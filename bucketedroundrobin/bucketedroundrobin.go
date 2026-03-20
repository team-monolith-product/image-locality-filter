package bucketedroundrobin

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/kubernetes/pkg/scheduler/framework"
)

const Name = "BucketedRoundRobin"

type BucketedRoundRobinArgs struct {
	BucketSize int32 `json:"bucketSize"`
}

type indexedScore struct {
	origIdx int
	score   int64
}

type BucketedRoundRobin struct {
	handle      framework.Handle
	bucketSize  int
	mu          sync.RWMutex
	recentNodes []string
}

var _ framework.ScorePlugin = &BucketedRoundRobin{}
var _ framework.ScoreExtensions = &BucketedRoundRobin{}
var _ framework.ReservePlugin = &BucketedRoundRobin{}

func New(obj runtime.Object, h framework.Handle) (framework.Plugin, error) {
	raw := obj.(*runtime.Unknown)
	var args BucketedRoundRobinArgs
	json.Unmarshal(raw.Raw, &args)
	return &BucketedRoundRobin{
		handle:     h,
		bucketSize: int(args.BucketSize),
	}, nil
}

func (pl *BucketedRoundRobin) Name() string {
	return Name
}

// k8s.io/kubernetes/pkg/scheduler/framework/plugins/noderesources/most_allocated.go
// 의 mostRequestedScore를 기반으로 하되, 다음을 변경:
//   - actualRequested()에서 component=user-placeholder 라벨 pod 제외
//   - calculatePodResourceRequest()에서 메모리는 MilliValue() 대신 Value() 사용
//     (node allocatable 단위와 일치시키기 위함)
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
	// 원본 mostRequestedScore와 동일한 divide-by-zero 방어
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

func (pl *BucketedRoundRobin) NormalizeScore(ctx context.Context, state *framework.CycleState, pod *v1.Pod, scores framework.NodeScoreList) *framework.Status {
	n := len(scores)
	if n == 0 {
		return nil
	}

	recentNodes := pl.getRecentNodes()

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

		// 버킷 내에서 recentNodes에 포함되지 않은 첫 번째 노드를 winner로 선택
		winnerOffset := pickWinner(scores, sorted[start:end], recentNodes)
		for i := start; i < end; i++ {
			posInBucket := i - start
			offset := (posInBucket - winnerOffset + bucketLen) % bucketLen
			scores[sorted[i].origIdx].Score = bucketBase - int64(offset)
		}
	}

	return nil
}

// pickWinner는 버킷 내에서 recentNodes에 포함되지 않은 첫 번째 노드의 offset을 반환한다.
// 모든 노드가 recentNodes에 포함되어 있으면 offset 0을 반환한다.
func pickWinner(scores framework.NodeScoreList, bucket []indexedScore, recentNodes []string) int {
	recentSet := make(map[string]struct{}, len(recentNodes))
	for _, name := range recentNodes {
		recentSet[name] = struct{}{}
	}
	for i, entry := range bucket {
		if _, recent := recentSet[scores[entry.origIdx].Name]; !recent {
			return i
		}
	}
	return 0
}

func (pl *BucketedRoundRobin) ScoreExtensions() framework.ScoreExtensions {
	return pl
}

func (pl *BucketedRoundRobin) Reserve(ctx context.Context, state *framework.CycleState, pod *v1.Pod, nodeName string) *framework.Status {
	pl.mu.Lock()
	defer pl.mu.Unlock()

	maxRecent := pl.bucketSize - 1
	if maxRecent <= 0 {
		return nil
	}

	pl.recentNodes = append(pl.recentNodes, nodeName)
	if len(pl.recentNodes) > maxRecent {
		pl.recentNodes = pl.recentNodes[len(pl.recentNodes)-maxRecent:]
	}
	return nil
}

func (pl *BucketedRoundRobin) Unreserve(ctx context.Context, state *framework.CycleState, pod *v1.Pod, nodeName string) {
}

func (pl *BucketedRoundRobin) getRecentNodes() []string {
	pl.mu.RLock()
	defer pl.mu.RUnlock()
	cp := make([]string, len(pl.recentNodes))
	copy(cp, pl.recentNodes)
	return cp
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
			switch resource {
			case v1.ResourceMemory:
				total += q.Value()
			default:
				total += q.MilliValue()
			}
		}
	}
	return total
}
