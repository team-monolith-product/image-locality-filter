// Package brrpreemption 은 버킷 기반 most-allocated 점수로
// preemption 대상 노드를 선택하는 스케줄러 PostFilter 플러그인이다.
package brrpreemption

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"

	v1 "k8s.io/api/core/v1"
	policy "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/runtime"
	corelisters "k8s.io/client-go/listers/core/v1"
	policylisters "k8s.io/client-go/listers/policy/v1"
	kubeschedulerconfigv1beta2 "k8s.io/kube-scheduler/config/v1beta2"
	extenderv1 "k8s.io/kube-scheduler/extender/v1"
	"k8s.io/kubernetes/pkg/scheduler/apis/config"
	configv1beta2 "k8s.io/kubernetes/pkg/scheduler/apis/config/v1beta2"
	"k8s.io/kubernetes/pkg/scheduler/apis/config/validation"
	"k8s.io/kubernetes/pkg/scheduler/framework"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/defaultpreemption"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/feature"
	"k8s.io/kubernetes/pkg/scheduler/framework/preemption"
	"k8s.io/kubernetes/pkg/scheduler/metrics"
)

const Name = "BucketedPreemption"

type BucketedPreemptionArgs struct {
	BucketSize                  int32 `json:"bucketSize"`
	MinCandidateNodesPercentage int32 `json:"minCandidateNodesPercentage"`
	MinCandidateNodesAbsolute   int32 `json:"minCandidateNodesAbsolute"`
}

type scoredCandidate struct {
	candidate preemption.Candidate
	projected int64
	alloc     int64
}

// k8s.io/kubernetes/pkg/scheduler/framework/plugins/defaultpreemption/default_preemption.go
// 구현을 그대로 재사용하되, 후보 노드 선택만 bucketed most-allocation 기준으로 바꾼다.
type BucketedPreemption struct {
	fh        framework.Handle
	base      *defaultpreemption.DefaultPreemption
	podLister corelisters.PodLister
	pdbLister policylisters.PodDisruptionBudgetLister

	bucketSize int

	mu             sync.RWMutex
	recentNodes    []string
	incomingMemory int64
}

var _ framework.PostFilterPlugin = &BucketedPreemption{}

func New(obj runtime.Object, h framework.Handle) (framework.Plugin, error) {
	args := BucketedPreemptionArgs{BucketSize: 4}
	if raw, ok := obj.(*runtime.Unknown); ok {
		_ = json.Unmarshal(raw.Raw, &args)
	}

	dpa := defaultDefaultPreemptionArgs()
	if args.MinCandidateNodesPercentage > 0 {
		dpa.MinCandidateNodesPercentage = args.MinCandidateNodesPercentage
	}
	if args.MinCandidateNodesAbsolute > 0 {
		dpa.MinCandidateNodesAbsolute = args.MinCandidateNodesAbsolute
	}
	if err := validation.ValidateDefaultPreemptionArgs(nil, &dpa); err != nil {
		return nil, err
	}

	basePlugin, err := defaultpreemption.New(&dpa, h, feature.Features{})
	if err != nil {
		return nil, err
	}

	bucketSize := int(args.BucketSize)
	if bucketSize <= 0 {
		bucketSize = 1
	}

	return &BucketedPreemption{
		fh:         h,
		base:       basePlugin.(*defaultpreemption.DefaultPreemption),
		podLister:  h.SharedInformerFactory().Core().V1().Pods().Lister(),
		pdbLister:  h.SharedInformerFactory().Policy().V1().PodDisruptionBudgets().Lister(),
		bucketSize: bucketSize,
	}, nil
}

func (pl *BucketedPreemption) Name() string {
	return Name
}

func (pl *BucketedPreemption) PostFilter(ctx context.Context, state *framework.CycleState, pod *v1.Pod, m framework.NodeToStatusMap) (*framework.PostFilterResult, *framework.Status) {
	defer func() {
		metrics.PreemptionAttempts.Inc()
	}()

	pl.setIncomingMemory(podMemoryRequest(pod))

	pe := preemption.Evaluator{
		PluginName: Name,
		Handler:    pl.fh,
		PodLister:  pl.podLister,
		PdbLister:  pl.pdbLister,
		State:      state,
		Interface:  pl,
	}

	result, status := pe.Preempt(ctx, pod, m)
	if status.IsSuccess() && result != nil && result.NominatingInfo != nil && result.NominatedNodeName != "" {
		pl.appendRecentNode(result.NominatedNodeName)
	}
	if status.Message() != "" {
		return result, framework.NewStatus(status.Code(), "preemption: "+status.Message())
	}
	return result, status
}

func (pl *BucketedPreemption) GetOffsetAndNumCandidates(numNodes int32) (int32, int32) {
	return pl.base.GetOffsetAndNumCandidates(numNodes)
}

func (pl *BucketedPreemption) CandidatesToVictimsMap(candidates []preemption.Candidate) map[string]*extenderv1.Victims {
	if len(candidates) == 0 {
		return map[string]*extenderv1.Victims{}
	}

	incomingMemory := pl.getIncomingMemory()
	scored := make([]scoredCandidate, 0, len(candidates))
	for _, c := range candidates {
		nodeInfo, err := pl.fh.SnapshotSharedLister().NodeInfos().Get(c.Name())
		if err != nil {
			continue
		}
		node := nodeInfo.Node()
		if node == nil {
			continue
		}

		allocMem := node.Status.Allocatable.Memory().Value()
		if allocMem <= 0 {
			allocMem = 1
		}

		requestedMem := actualRequested(nodeInfo)
		victimMem := victimsMemoryRequest(c.Victims().Pods)
		projectedMem := requestedMem - victimMem + incomingMemory
		if projectedMem < 0 {
			projectedMem = 0
		}

		scored = append(scored, scoredCandidate{
			candidate: c,
			projected: projectedMem,
			alloc:     allocMem,
		})
	}

	if len(scored) == 0 {
		fallback := candidates[0]
		return map[string]*extenderv1.Victims{fallback.Name(): fallback.Victims()}
	}

	sort.SliceStable(scored, func(i, j int) bool {
		left := float64(scored[i].projected) / float64(scored[i].alloc)
		right := float64(scored[j].projected) / float64(scored[j].alloc)
		if left == right {
			if scored[i].projected == scored[j].projected {
				return scored[i].candidate.Name() < scored[j].candidate.Name()
			}
			return scored[i].projected > scored[j].projected
		}
		return left > right
	})

	bucketSize := pl.bucketSize
	if bucketSize > len(scored) {
		bucketSize = len(scored)
	}
	bucket := scored[:bucketSize]
	winner := pl.pickWinner(bucket)

	return map[string]*extenderv1.Victims{winner.Name(): winner.Victims()}
}

func (pl *BucketedPreemption) PodEligibleToPreemptOthers(pod *v1.Pod, nominatedNodeStatus *framework.Status) (bool, string) {
	return pl.base.PodEligibleToPreemptOthers(pod, nominatedNodeStatus)
}

func (pl *BucketedPreemption) SelectVictimsOnNode(
	ctx context.Context,
	state *framework.CycleState,
	pod *v1.Pod,
	nodeInfo *framework.NodeInfo,
	pdbs []*policy.PodDisruptionBudget,
) ([]*v1.Pod, int, *framework.Status) {
	return pl.base.SelectVictimsOnNode(ctx, state, pod, nodeInfo, pdbs)
}

func (pl *BucketedPreemption) pickWinner(bucket []scoredCandidate) preemption.Candidate {
	recent := pl.getRecentNodes()
	recentSet := make(map[string]struct{}, len(recent))
	for _, name := range recent {
		recentSet[name] = struct{}{}
	}

	for _, c := range bucket {
		if _, exists := recentSet[c.candidate.Name()]; !exists {
			return c.candidate
		}
	}
	return bucket[0].candidate
}

func (pl *BucketedPreemption) appendRecentNode(nodeName string) {
	pl.mu.Lock()
	defer pl.mu.Unlock()

	maxRecent := pl.bucketSize - 1
	if maxRecent <= 0 {
		return
	}

	pl.recentNodes = append(pl.recentNodes, nodeName)
	if len(pl.recentNodes) > maxRecent {
		pl.recentNodes = pl.recentNodes[len(pl.recentNodes)-maxRecent:]
	}
}

func (pl *BucketedPreemption) getRecentNodes() []string {
	pl.mu.RLock()
	defer pl.mu.RUnlock()
	cp := make([]string, len(pl.recentNodes))
	copy(cp, pl.recentNodes)
	return cp
}

func (pl *BucketedPreemption) setIncomingMemory(memory int64) {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	pl.incomingMemory = memory
}

func (pl *BucketedPreemption) getIncomingMemory() int64 {
	pl.mu.RLock()
	defer pl.mu.RUnlock()
	return pl.incomingMemory
}

func defaultDefaultPreemptionArgs() config.DefaultPreemptionArgs {
	v1beta2 := &kubeschedulerconfigv1beta2.DefaultPreemptionArgs{}
	configv1beta2.SetDefaults_DefaultPreemptionArgs(v1beta2)
	args := &config.DefaultPreemptionArgs{}
	_ = configv1beta2.Convert_v1beta2_DefaultPreemptionArgs_To_config_DefaultPreemptionArgs(v1beta2, args, nil)
	return *args
}

func actualRequested(nodeInfo *framework.NodeInfo) int64 {
	var memory int64
	for _, podInfo := range nodeInfo.Pods {
		if podInfo.Pod.Labels["component"] == "user-placeholder" {
			continue
		}
		memory += podMemoryRequest(podInfo.Pod)
	}
	return memory
}

func victimsMemoryRequest(victims []*v1.Pod) int64 {
	var total int64
	for _, pod := range victims {
		if pod.Labels["component"] == "user-placeholder" {
			continue
		}
		total += podMemoryRequest(pod)
	}
	return total
}

func podMemoryRequest(pod *v1.Pod) int64 {
	var total int64
	for _, container := range pod.Spec.Containers {
		if q, ok := container.Resources.Requests[v1.ResourceMemory]; ok {
			total += q.Value()
		}
	}
	return total
}

func (pl *BucketedPreemption) String() string {
	return fmt.Sprintf("%s(bucketSize=%d)", Name, pl.bucketSize)
}
