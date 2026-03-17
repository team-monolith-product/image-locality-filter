package bucketedroundrobin

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync/atomic"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/kubernetes/pkg/scheduler/framework"
)

const Name = "BucketedRoundRobin"

type BucketedRoundRobinArgs struct {
	BucketSize int32 `json:"bucketSize"`
}

type BucketedRoundRobin struct {
	handle     framework.Handle
	bucketSize int
	counter    atomic.Uint64
}

var _ framework.ScorePlugin = &BucketedRoundRobin{}
var _ framework.ScoreExtensions = &BucketedRoundRobin{}

func New(obj runtime.Object, h framework.Handle) (framework.Plugin, error) {
	args := BucketedRoundRobinArgs{BucketSize: 1}
	if obj != nil {
		raw, ok := obj.(*runtime.Unknown)
		if !ok {
			return nil, fmt.Errorf("expected *runtime.Unknown, got %T", obj)
		}
		if err := json.Unmarshal(raw.Raw, &args); err != nil {
			return nil, fmt.Errorf("failed to parse BucketedRoundRobin args: %w", err)
		}
	}
	if args.BucketSize <= 0 {
		return nil, fmt.Errorf("bucketSize must be positive, got %d", args.BucketSize)
	}
	return &BucketedRoundRobin{
		handle:     h,
		bucketSize: int(args.BucketSize),
	}, nil
}

func (pl *BucketedRoundRobin) Name() string {
	return Name
}

// Score calculates a MostAllocated-style utilization score per node,
// excluding placeholder pods from the calculation.
func (pl *BucketedRoundRobin) Score(ctx context.Context, state *framework.CycleState, pod *v1.Pod, nodeName string) (int64, *framework.Status) {
	nodeInfo, err := pl.handle.SnapshotSharedLister().NodeInfos().Get(nodeName)
	if err != nil {
		return 0, framework.NewStatus(framework.Error, fmt.Sprintf("getting node %q info: %v", nodeName, err))
	}

	node := nodeInfo.Node()
	if node == nil {
		return 0, framework.NewStatus(framework.Error, "node not found")
	}

	allocCPU := node.Status.Allocatable.Cpu().MilliValue()
	allocMem := node.Status.Allocatable.Memory().Value()
	if allocCPU == 0 || allocMem == 0 {
		return 0, nil
	}

	reqCPU, reqMem := actualRequested(nodeInfo)
	reqCPU += calculatePodResourceRequest(pod, v1.ResourceCPU)
	reqMem += calculatePodResourceRequest(pod, v1.ResourceMemory)

	cpuScore := (reqCPU * framework.MaxNodeScore) / allocCPU
	memScore := (reqMem * framework.MaxNodeScore) / allocMem

	return (cpuScore + memScore) / 2, nil
}

// NormalizeScore groups nodes into buckets by raw score and applies
// round-robin within each bucket so that nodes with similar utilization
// take turns being the highest scored.
func (pl *BucketedRoundRobin) NormalizeScore(ctx context.Context, state *framework.CycleState, pod *v1.Pod, scores framework.NodeScoreList) *framework.Status {
	n := len(scores)
	if n == 0 {
		return nil
	}

	cycle := pl.counter.Add(1) - 1

	type indexedScore struct {
		origIdx int
		score   int64
	}
	sorted := make([]indexedScore, n)
	for i, s := range scores {
		sorted[i] = indexedScore{origIdx: i, score: s.Score}
	}
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].score > sorted[j].score
	})

	bucketSize := pl.bucketSize
	totalBuckets := (n + bucketSize - 1) / bucketSize
	scorePerBucket := int64(0)
	if totalBuckets > 1 {
		scorePerBucket = framework.MaxNodeScore / int64(totalBuckets)
	}

	for bucketIdx := 0; bucketIdx < totalBuckets; bucketIdx++ {
		start := bucketIdx * bucketSize
		end := start + bucketSize
		if end > n {
			end = n
		}
		bucketLen := end - start

		bucketBase := framework.MaxNodeScore - int64(bucketIdx)*scorePerBucket
		if totalBuckets == 1 {
			bucketBase = framework.MaxNodeScore
		}

		winnerOffset := int(cycle % uint64(bucketLen))
		for i := start; i < end; i++ {
			posInBucket := i - start
			offset := (posInBucket - winnerOffset + bucketLen) % bucketLen
			scores[sorted[i].origIdx].Score = bucketBase - int64(offset)
		}
	}

	return nil
}

func (pl *BucketedRoundRobin) ScoreExtensions() framework.ScoreExtensions {
	return pl
}

func actualRequested(nodeInfo *framework.NodeInfo) (milliCPU, memory int64) {
	for _, podInfo := range nodeInfo.Pods {
		if podInfo.Pod.Labels["component"] == "user-placeholder" {
			continue
		}
		for _, container := range podInfo.Pod.Spec.Containers {
			milliCPU += container.Resources.Requests.Cpu().MilliValue()
			memory += container.Resources.Requests.Memory().Value()
		}
	}
	return
}

func calculatePodResourceRequest(pod *v1.Pod, resource v1.ResourceName) int64 {
	var total int64
	for _, container := range pod.Spec.Containers {
		if q, ok := container.Resources.Requests[resource]; ok {
			total += q.MilliValue()
		}
	}
	return total
}
