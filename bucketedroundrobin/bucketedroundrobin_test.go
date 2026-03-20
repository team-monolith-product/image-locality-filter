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

func TestActualRequestedExcludesPlaceholder(t *testing.T) {
	nodeInfo := framework.NewNodeInfo(
		makePod("real", 1000, 1024, nil),
		makePod("placeholder", 500, 512, map[string]string{"component": "user-placeholder"}),
	)
	mem := actualRequested(nodeInfo)
	if mem != 1024 {
		t.Errorf("expected mem=1024, got %d", mem)
	}
}

func TestNormalizeScoreBucketSize1(t *testing.T) {
	raw, _ := json.Marshal(BucketedRoundRobinArgs{BucketSize: 1})
	obj := &runtime.Unknown{Raw: raw}
	p, _ := New(obj, nil)
	brr := p.(*BucketedRoundRobin)

	scores := framework.NodeScoreList{
		{Name: "low", Score: 20},
		{Name: "high", Score: 80},
		{Name: "mid", Score: 50},
	}

	brr.NormalizeScore(context.Background(), nil, nil, scores)

	scoreMap := map[string]int64{}
	for _, s := range scores {
		scoreMap[s.Name] = s.Score
	}
	if scoreMap["high"] <= scoreMap["mid"] || scoreMap["mid"] <= scoreMap["low"] {
		t.Errorf("expected high > mid > low, got high=%d mid=%d low=%d",
			scoreMap["high"], scoreMap["mid"], scoreMap["low"])
	}
}

// bucketSize=4, 동점 4노드에서 Reserve를 포함한 4 cycle 시뮬레이션.
// recentNodes가 쌓이면서 각 노드가 정확히 1번씩 winner가 되어야 한다.
func TestRoundRobinWithReserve(t *testing.T) {
	raw, _ := json.Marshal(BucketedRoundRobinArgs{BucketSize: 4})
	obj := &runtime.Unknown{Raw: raw}
	p, _ := New(obj, nil)
	brr := p.(*BucketedRoundRobin)
	pod := makePod("incoming", 500, 1024, nil)

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
		brr.Reserve(context.Background(), nil, pod, best)
		winners[best]++
	}

	for _, name := range []string{"a", "b", "c", "d"} {
		if winners[name] != 1 {
			t.Errorf("expected node %s to win once, won %d times. winners: %v", name, winners[name], winners)
		}
	}
}

func TestSimulateEmpty8Nodes24Pods(t *testing.T) {
	raw, _ := json.Marshal(BucketedRoundRobinArgs{BucketSize: 4})
	obj := &runtime.Unknown{Raw: raw}
	p, _ := New(obj, nil)
	brr := p.(*BucketedRoundRobin)

	nodes := []string{"a", "b", "c", "d", "e", "f", "g", "h"}
	counts := map[string]int{}
	pod := makePod("incoming", 500, 1024, nil)

	for i := 0; i < 24; i++ {
		scores := make(framework.NodeScoreList, len(nodes))
		for j, name := range nodes {
			scores[j] = framework.NodeScore{Name: name, Score: int64(counts[name])}
		}

		status := brr.NormalizeScore(context.Background(), nil, nil, scores)
		if status != nil {
			t.Fatalf("normalize returned status: %v", status)
		}

		winner := scores[0]
		for _, s := range scores[1:] {
			if s.Score > winner.Score {
				winner = s
			}
		}

		brr.Reserve(context.Background(), nil, pod, winner.Name)
		counts[winner.Name]++
	}

	expected := map[string]int{
		"a": 6, "b": 6, "c": 6, "d": 6,
		"e": 0, "f": 0, "g": 0, "h": 0,
	}

	for _, name := range nodes {
		if counts[name] != expected[name] {
			t.Fatalf("unexpected distribution for %s: got %d, want %d, all=%v", name, counts[name], expected[name], counts)
		}
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
	pod := makePod("incoming", 500, 1024, nil)

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
		brr.Reserve(context.Background(), nil, pod, best)
		winners[best]++
	}

	if winners["a"] != 1 || winners["b"] != 1 {
		t.Errorf("expected each node to win once, got: %v", winners)
	}
}

func TestNormalizeScoreSingleNode(t *testing.T) {
	raw, _ := json.Marshal(BucketedRoundRobinArgs{BucketSize: 1})
	obj := &runtime.Unknown{Raw: raw}
	p, _ := New(obj, nil)
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
	raw, _ := json.Marshal(BucketedRoundRobinArgs{BucketSize: 1})
	obj := &runtime.Unknown{Raw: raw}
	p, _ := New(obj, nil)
	brr := p.(*BucketedRoundRobin)

	scores := framework.NodeScoreList{}
	status := brr.NormalizeScore(context.Background(), nil, nil, scores)
	if status != nil {
		t.Errorf("expected nil status for empty scores, got %v", status)
	}
}

func TestPodMemoryRequest(t *testing.T) {
	pod := makePod("test", 500, 1024, nil)
	mem := podMemoryRequest(pod)
	if mem != 1024 {
		t.Errorf("expected 1024 bytes memory, got %d", mem)
	}
}
