package bucketedroundrobin

import (
	"context"
	"encoding/json"
	"testing"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/kubernetes/pkg/scheduler/framework"
)

func makeNode(name string, cpuMillis int64, memBytes int64) *v1.Node {
	return &v1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: v1.NodeStatus{
			Allocatable: v1.ResourceList{
				v1.ResourceCPU:    *resource.NewMilliQuantity(cpuMillis, resource.DecimalSI),
				v1.ResourceMemory: *resource.NewQuantity(memBytes, resource.BinarySI),
			},
		},
	}
}

func makePod(name string, cpuMillis int64, memBytes int64, labels map[string]string) *v1.Pod {
	return &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name: "main",
					Resources: v1.ResourceRequirements{
						Requests: v1.ResourceList{
							v1.ResourceCPU:    *resource.NewMilliQuantity(cpuMillis, resource.DecimalSI),
							v1.ResourceMemory: *resource.NewQuantity(memBytes, resource.BinarySI),
						},
					},
				},
			},
		},
	}
}

func TestNewDefaultArgs(t *testing.T) {
	p, err := New(nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	brr := p.(*BucketedRoundRobin)
	if brr.Name() != Name {
		t.Errorf("expected plugin name %s, got %s", Name, brr.Name())
	}
	if brr.bucketSize != 1 {
		t.Errorf("expected bucketSize=1, got %d", brr.bucketSize)
	}
}

func TestNewWithArgs(t *testing.T) {
	raw, _ := json.Marshal(BucketedRoundRobinArgs{BucketSize: 4})
	obj := &runtime.Unknown{Raw: raw}
	p, err := New(obj, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	brr := p.(*BucketedRoundRobin)
	if brr.bucketSize != 4 {
		t.Errorf("expected bucketSize=4, got %d", brr.bucketSize)
	}
}

func TestNewInvalidBucketSize(t *testing.T) {
	raw, _ := json.Marshal(BucketedRoundRobinArgs{BucketSize: 0})
	obj := &runtime.Unknown{Raw: raw}
	_, err := New(obj, nil)
	if err == nil {
		t.Fatal("expected error for bucketSize=0")
	}
}

func TestActualRequestedExcludesPlaceholder(t *testing.T) {
	nodeInfo := framework.NewNodeInfo(
		makePod("real", 1000, 1024, nil),
		makePod("placeholder", 500, 512, map[string]string{"component": "user-placeholder"}),
	)
	cpu, mem := actualRequested(nodeInfo)
	if cpu != 1000 {
		t.Errorf("expected cpu=1000, got %d", cpu)
	}
	if mem != 1024 {
		t.Errorf("expected mem=1024, got %d", mem)
	}
}

func TestNormalizeScoreBucketSize1(t *testing.T) {
	// bucketSize=1 -> pure MostAllocated ordering, no round-robin
	p, _ := New(nil, nil)
	brr := p.(*BucketedRoundRobin)

	scores := framework.NodeScoreList{
		{Name: "low", Score: 20},
		{Name: "high", Score: 80},
		{Name: "mid", Score: 50},
	}

	brr.NormalizeScore(context.Background(), nil, nil, scores)

	// high should always get MaxNodeScore
	scoreMap := map[string]int64{}
	for _, s := range scores {
		scoreMap[s.Name] = s.Score
	}
	if scoreMap["high"] <= scoreMap["mid"] || scoreMap["mid"] <= scoreMap["low"] {
		t.Errorf("expected high > mid > low, got high=%d mid=%d low=%d",
			scoreMap["high"], scoreMap["mid"], scoreMap["low"])
	}
}

func TestNormalizeScoreRoundRobin(t *testing.T) {
	raw, _ := json.Marshal(BucketedRoundRobinArgs{BucketSize: 4})
	obj := &runtime.Unknown{Raw: raw}

	// 4 nodes with same score -> single bucket, round-robin
	p, _ := New(obj, nil)
	brr := p.(*BucketedRoundRobin)

	winners := map[string]int{}
	for i := 0; i < 4; i++ {
		scores := framework.NodeScoreList{
			{Name: "a", Score: 50},
			{Name: "b", Score: 50},
			{Name: "c", Score: 50},
			{Name: "d", Score: 50},
		}
		brr.NormalizeScore(context.Background(), nil, nil, scores)

		var best string
		var bestScore int64 = -1
		for _, s := range scores {
			if s.Score > bestScore {
				bestScore = s.Score
				best = s.Name
			}
		}
		winners[best]++
	}

	// Each node should win exactly once in 4 cycles
	for _, name := range []string{"a", "b", "c", "d"} {
		if winners[name] != 1 {
			t.Errorf("expected node %s to win once, won %d times. winners: %v", name, winners[name], winners)
		}
	}
}

func TestNormalizeScoreSkipsLastSelectedNode(t *testing.T) {
	raw, _ := json.Marshal(BucketedRoundRobinArgs{BucketSize: 4})
	obj := &runtime.Unknown{Raw: raw}
	p, _ := New(obj, nil)
	brr := p.(*BucketedRoundRobin)

	status := brr.Reserve(context.Background(), nil, makePod("prev", 500, 1024, nil), "a")
	if status != nil {
		t.Fatalf("reserve returned status: %v", status)
	}

	scores := framework.NodeScoreList{
		{Name: "a", Score: 50},
		{Name: "b", Score: 50},
		{Name: "c", Score: 50},
		{Name: "d", Score: 50},
	}
	brr.NormalizeScore(context.Background(), nil, nil, scores)

	var best string
	var bestScore int64 = -1
	for _, s := range scores {
		if s.Score > bestScore {
			bestScore = s.Score
			best = s.Name
		}
	}
	if best == "a" {
		t.Fatalf("expected last selected node 'a' to be skipped as winner, got scores: %+v", scores)
	}
}

func TestNormalizeScoreMultipleBuckets(t *testing.T) {
	raw, _ := json.Marshal(BucketedRoundRobinArgs{BucketSize: 2})
	obj := &runtime.Unknown{Raw: raw}
	p, _ := New(obj, nil)
	brr := p.(*BucketedRoundRobin)

	scores := framework.NodeScoreList{
		{Name: "high1", Score: 90},
		{Name: "high2", Score: 80},
		{Name: "low1", Score: 20},
		{Name: "low2", Score: 10},
	}

	brr.NormalizeScore(context.Background(), nil, nil, scores)

	scoreMap := map[string]int64{}
	for _, s := range scores {
		scoreMap[s.Name] = s.Score
	}

	// High bucket nodes should always score higher than low bucket nodes
	highMin := scoreMap["high1"]
	if scoreMap["high2"] < highMin {
		highMin = scoreMap["high2"]
	}
	lowMax := scoreMap["low1"]
	if scoreMap["low2"] > lowMax {
		lowMax = scoreMap["low2"]
	}
	if highMin <= lowMax {
		t.Errorf("high bucket min (%d) should be > low bucket max (%d). scores: %v",
			highMin, lowMax, scoreMap)
	}
}

func TestNormalizeScoreNodesLessThanBucketSize(t *testing.T) {
	raw, _ := json.Marshal(BucketedRoundRobinArgs{BucketSize: 4})
	obj := &runtime.Unknown{Raw: raw}
	p, _ := New(obj, nil)
	brr := p.(*BucketedRoundRobin)

	// 2 nodes, bucketSize=4 -> single bucket of 2, round-robin within
	winners := map[string]int{}
	for i := 0; i < 2; i++ {
		scores := framework.NodeScoreList{
			{Name: "a", Score: 50},
			{Name: "b", Score: 50},
		}
		brr.NormalizeScore(context.Background(), nil, nil, scores)

		var best string
		var bestScore int64 = -1
		for _, s := range scores {
			if s.Score > bestScore {
				bestScore = s.Score
				best = s.Name
			}
		}
		winners[best]++
	}

	if winners["a"] != 1 || winners["b"] != 1 {
		t.Errorf("expected each node to win once, got: %v", winners)
	}
}

func TestNormalizeScoreSingleNode(t *testing.T) {
	p, _ := New(nil, nil)
	brr := p.(*BucketedRoundRobin)

	scores := framework.NodeScoreList{
		{Name: "only", Score: 50},
	}
	brr.NormalizeScore(context.Background(), nil, nil, scores)

	if scores[0].Score != framework.MaxNodeScore {
		t.Errorf("expected MaxNodeScore, got %d", scores[0].Score)
	}
}

func TestNormalizeScoreEmpty(t *testing.T) {
	p, _ := New(nil, nil)
	brr := p.(*BucketedRoundRobin)

	scores := framework.NodeScoreList{}
	status := brr.NormalizeScore(context.Background(), nil, nil, scores)
	if status != nil {
		t.Errorf("expected nil status for empty scores, got %v", status)
	}
}

func TestCalculatePodResourceRequest(t *testing.T) {
	pod := makePod("test", 500, 1024, nil)
	cpu := calculatePodResourceRequest(pod, v1.ResourceCPU)
	// CPU uses MilliValue() which returns milliCPU
	if cpu != 500 {
		t.Errorf("expected 500m cpu, got %d", cpu)
	}

	mem := calculatePodResourceRequest(pod, v1.ResourceMemory)
	if mem != 1024 {
		t.Errorf("expected 1024 bytes memory, got %d", mem)
	}
}
