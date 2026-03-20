// k8s.io/kubernetes@v1.25.7/pkg/scheduler/framework/plugins/noderesources/fit.go 를
// 래핑한다. 빌트인 NodeResourcesFit의 PreFilter/Score를 그대로 위임하고,
// Filter에서만 placeholder pod의 리소스를 nodeInfo.Requested에서 차감한 뒤
// 빌트인 Filter를 호출한다.
//
// 빌트인 NodeResourcesFit는 config에서 disable하고, 이 플러그인을 같은 extension
// point(PreFilter, Filter, Score)에 enable해야 한다.
package placeholderawarefit

import (
	"context"
	"encoding/json"
	"fmt"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/scheduler/apis/config"
	"k8s.io/kubernetes/pkg/scheduler/framework"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/feature"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/noderesources"
)

const Name = "PlaceholderAwareNodeResourcesFit"

// inner는 빌트인 Fit 인스턴스를 들고 있다.
// PreFilter, Score는 inner에 위임하고, Filter만 보정 후 위임한다.
type PlaceholderAwareFit struct {
	inner framework.Plugin
}

var _ framework.PreFilterPlugin = &PlaceholderAwareFit{}
var _ framework.FilterPlugin = &PlaceholderAwareFit{}
var _ framework.ScorePlugin = &PlaceholderAwareFit{}
var _ framework.EnqueueExtensions = &PlaceholderAwareFit{}

func New(plArgs runtime.Object, h framework.Handle, fts feature.Features) (framework.Plugin, error) {
	// out-of-tree 플러그인은 프레임워크가 args를 자동 디코딩하지 않으므로
	// *runtime.Unknown에서 *config.NodeResourcesFitArgs로 직접 변환한다.
	args, err := decodeArgs(plArgs)
	if err != nil {
		return nil, err
	}
	inner, err := noderesources.NewFit(args, h, fts)
	if err != nil {
		return nil, err
	}
	return &PlaceholderAwareFit{inner: inner}, nil
}

func decodeArgs(obj runtime.Object) (*config.NodeResourcesFitArgs, error) {
	raw, ok := obj.(*runtime.Unknown)
	if !ok {
		return nil, fmt.Errorf("expected *runtime.Unknown, got %T", obj)
	}
	args := &config.NodeResourcesFitArgs{}
	if err := json.Unmarshal(raw.Raw, args); err != nil {
		return nil, fmt.Errorf("failed to unmarshal NodeResourcesFitArgs: %w", err)
	}
	return args, nil
}

func (pl *PlaceholderAwareFit) Name() string {
	return Name
}

// PreFilter는 빌트인 Fit의 PreFilter를 그대로 호출한다.
// incoming pod의 리소스 요청량을 CycleState에 저장하는 역할.
func (pl *PlaceholderAwareFit) PreFilter(ctx context.Context, cycleState *framework.CycleState, pod *v1.Pod) (*framework.PreFilterResult, *framework.Status) {
	return pl.inner.(framework.PreFilterPlugin).PreFilter(ctx, cycleState, pod)
}

func (pl *PlaceholderAwareFit) PreFilterExtensions() framework.PreFilterExtensions {
	return pl.inner.(framework.PreFilterPlugin).PreFilterExtensions()
}

// Filter는 user pod일 때 nodeInfo를 복제해서 placeholder 리소스를 차감한 뒤
// 빌트인 Filter에 전달한다. placeholder pod은 보정 없이 원본 그대로 전달한다.
func (pl *PlaceholderAwareFit) Filter(ctx context.Context, cycleState *framework.CycleState, pod *v1.Pod, nodeInfo *framework.NodeInfo) *framework.Status {
	node := nodeInfo.Node()
	nodeName := ""
	if node != nil {
		nodeName = node.Name
	}
	klog.V(4).InfoS("PlaceholderAwareFit.Filter",
		"node", nodeName,
		"requestedMilliCPU", nodeInfo.Requested.MilliCPU,
		"requestedMemory", nodeInfo.Requested.Memory,
		"pods", len(nodeInfo.Pods),
		"isPlaceholder", isPlaceholderPod(pod),
	)

	if isPlaceholderPod(pod) {
		return pl.inner.(framework.FilterPlugin).Filter(ctx, cycleState, pod, nodeInfo)
	}
	adjusted := subtractPlaceholder(nodeInfo)

	klog.V(4).InfoS("PlaceholderAwareFit.Filter after subtract",
		"node", nodeName,
		"adjustedMilliCPU", adjusted.Requested.MilliCPU,
		"adjustedMemory", adjusted.Requested.Memory,
		"adjustedPods", len(adjusted.Pods),
	)

	return pl.inner.(framework.FilterPlugin).Filter(ctx, cycleState, pod, adjusted)
}

func (pl *PlaceholderAwareFit) Score(ctx context.Context, state *framework.CycleState, pod *v1.Pod, nodeName string) (int64, *framework.Status) {
	return pl.inner.(framework.ScorePlugin).Score(ctx, state, pod, nodeName)
}

func (pl *PlaceholderAwareFit) ScoreExtensions() framework.ScoreExtensions {
	return pl.inner.(framework.ScorePlugin).ScoreExtensions()
}

func (pl *PlaceholderAwareFit) EventsToRegister() []framework.ClusterEvent {
	return pl.inner.(framework.EnqueueExtensions).EventsToRegister()
}

func subtractPlaceholder(nodeInfo *framework.NodeInfo) *framework.NodeInfo {
	clone := nodeInfo.Clone()

	var kept []*framework.PodInfo
	for _, pi := range clone.Pods {
		if isPlaceholderPod(pi.Pod) {
			res := computePodResources(pi.Pod)
			clone.Requested.MilliCPU -= res.milliCPU
			clone.Requested.Memory -= res.memory
			clone.Requested.EphemeralStorage -= res.ephemeralStorage
			for rName, rValue := range res.scalarResources {
				clone.Requested.ScalarResources[rName] -= rValue
			}
		} else {
			kept = append(kept, pi)
		}
	}
	clone.Pods = kept
	return clone
}

type podResources struct {
	milliCPU         int64
	memory           int64
	ephemeralStorage int64
	scalarResources  map[v1.ResourceName]int64
}

// computePodResources는 framework.NodeInfo.AddPod에서 사용하는
// computePodResourceRequest와 동일한 방식으로 pod의 리소스를 계산한다.
// (containers 합산, initContainers max, Overhead 가산)
func computePodResources(pod *v1.Pod) podResources {
	var r podResources
	r.scalarResources = make(map[v1.ResourceName]int64)

	for _, c := range pod.Spec.Containers {
		r.milliCPU += c.Resources.Requests.Cpu().MilliValue()
		r.memory += c.Resources.Requests.Memory().Value()
		r.ephemeralStorage += c.Resources.Requests.StorageEphemeral().Value()
		for rName, rQuant := range c.Resources.Requests {
			if rName == v1.ResourceCPU || rName == v1.ResourceMemory || rName == v1.ResourceEphemeralStorage {
				continue
			}
			r.scalarResources[rName] += rQuant.Value()
		}
	}

	for _, c := range pod.Spec.InitContainers {
		if v := c.Resources.Requests.Cpu().MilliValue(); v > r.milliCPU {
			r.milliCPU = v
		}
		if v := c.Resources.Requests.Memory().Value(); v > r.memory {
			r.memory = v
		}
		if v := c.Resources.Requests.StorageEphemeral().Value(); v > r.ephemeralStorage {
			r.ephemeralStorage = v
		}
		for rName, rQuant := range c.Resources.Requests {
			if rName == v1.ResourceCPU || rName == v1.ResourceMemory || rName == v1.ResourceEphemeralStorage {
				continue
			}
			if v := rQuant.Value(); v > r.scalarResources[rName] {
				r.scalarResources[rName] = v
			}
		}
	}

	if pod.Spec.Overhead != nil {
		r.milliCPU += pod.Spec.Overhead.Cpu().MilliValue()
		r.memory += pod.Spec.Overhead.Memory().Value()
		r.ephemeralStorage += pod.Spec.Overhead.StorageEphemeral().Value()
		for rName, rQuant := range pod.Spec.Overhead {
			if rName == v1.ResourceCPU || rName == v1.ResourceMemory || rName == v1.ResourceEphemeralStorage {
				continue
			}
			r.scalarResources[rName] += rQuant.Value()
		}
	}

	return r
}

func isPlaceholderPod(pod *v1.Pod) bool {
	return pod.Labels["component"] == "user-placeholder"
}
