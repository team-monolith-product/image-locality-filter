package placeholderawarefit

import (
	"context"
	"fmt"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/kubernetes/pkg/scheduler/framework"
)

const Name = "PlaceholderAwareNodeResourcesFit"

type PlaceholderAwareNodeResourcesFit struct{}

var _ framework.FilterPlugin = &PlaceholderAwareNodeResourcesFit{}

func New(_ runtime.Object, _ framework.Handle) (framework.Plugin, error) {
	return &PlaceholderAwareNodeResourcesFit{}, nil
}

func (pl *PlaceholderAwareNodeResourcesFit) Name() string {
	return Name
}

// Filter checks whether the incoming pod fits on the node.
//
// For user pods (non-placeholder), the fit calculation uses nodeInfo.Requested
// (which reflects all pods including assumed ones) minus the placeholder
// contribution computed from nodeInfo.Pods. This ensures that:
//   - Assumed user pods are counted via nodeInfo.Requested (updated by AddPod)
//   - Placeholder pods are excluded so that nodes full of placeholders remain
//     feasible for user pods that will preempt them
//
// For placeholder pods, fit is calculated using the full nodeInfo.Requested
// without any subtraction.
func (pl *PlaceholderAwareNodeResourcesFit) Filter(ctx context.Context, state *framework.CycleState, pod *v1.Pod, nodeInfo *framework.NodeInfo) *framework.Status {
	node := nodeInfo.Node()
	if node == nil {
		return framework.NewStatus(framework.Error, "node not found")
	}

	alloc := node.Status.Allocatable

	// Start from nodeInfo.Requested which includes all pods (real + placeholder + assumed).
	// This is the value maintained by the scheduler cache via AddPod/RemovePod.
	usedCPU := nodeInfo.Requested.MilliCPU
	usedMem := nodeInfo.Requested.Memory
	usedEph := nodeInfo.Requested.EphemeralStorage
	usedPods := len(nodeInfo.Pods)

	// For user pods, subtract placeholder resource usage so that placeholder-full
	// nodes remain feasible. Placeholder pods themselves use the full Requested.
	if !isPlaceholderPod(pod) {
		for _, podInfo := range nodeInfo.Pods {
			if isPlaceholderPod(podInfo.Pod) {
				cpu, mem, eph, _ := podRequest(podInfo.Pod)
				usedCPU -= cpu
				usedMem -= mem
				usedEph -= eph
				usedPods--
			}
		}
	}

	podCPU, podMem, podEph, podOthers := podRequest(pod)

	if podCPU > alloc.Cpu().MilliValue()-usedCPU {
		return framework.NewStatus(framework.Unschedulable, fmt.Sprintf("insufficient cpu on %s", node.Name))
	}
	if podMem > alloc.Memory().Value()-usedMem {
		return framework.NewStatus(framework.Unschedulable, fmt.Sprintf("insufficient memory on %s", node.Name))
	}
	if podEph > alloc.StorageEphemeral().Value()-usedEph {
		return framework.NewStatus(framework.Unschedulable, fmt.Sprintf("insufficient ephemeral-storage on %s", node.Name))
	}

	for name, requested := range podOthers {
		allocQty, ok := alloc[name]
		if !ok {
			return framework.NewStatus(framework.Unschedulable, fmt.Sprintf("insufficient %s on %s", name, node.Name))
		}
		if requested > allocQty.Value() {
			return framework.NewStatus(framework.Unschedulable, fmt.Sprintf("insufficient %s on %s", name, node.Name))
		}
	}

	if allocPods, ok := alloc[v1.ResourcePods]; ok {
		if int64(usedPods)+1 > allocPods.Value() {
			return framework.NewStatus(framework.Unschedulable, fmt.Sprintf("insufficient pods on %s", node.Name))
		}
	}

	return nil
}

func podRequest(pod *v1.Pod) (cpuMilli int64, memBytes int64, ephBytes int64, others map[v1.ResourceName]int64) {
	others = make(map[v1.ResourceName]int64)

	for _, container := range pod.Spec.Containers {
		cpuMilli += requestValue(container.Resources.Requests, v1.ResourceCPU)
		memBytes += requestValue(container.Resources.Requests, v1.ResourceMemory)
		ephBytes += requestValue(container.Resources.Requests, v1.ResourceEphemeralStorage)
		addOtherRequests(others, container.Resources.Requests)
	}

	var initCPU int64
	var initMem int64
	var initEph int64
	initOthers := make(map[v1.ResourceName]int64)
	for _, container := range pod.Spec.InitContainers {
		reqCPU := requestValue(container.Resources.Requests, v1.ResourceCPU)
		if reqCPU > initCPU {
			initCPU = reqCPU
		}
		reqMem := requestValue(container.Resources.Requests, v1.ResourceMemory)
		if reqMem > initMem {
			initMem = reqMem
		}
		reqEph := requestValue(container.Resources.Requests, v1.ResourceEphemeralStorage)
		if reqEph > initEph {
			initEph = reqEph
		}
		for name, quantity := range container.Resources.Requests {
			if isBuiltInResource(name) {
				continue
			}
			value := quantity.Value()
			if value > initOthers[name] {
				initOthers[name] = value
			}
		}
	}
	if initCPU > cpuMilli {
		cpuMilli = initCPU
	}
	if initMem > memBytes {
		memBytes = initMem
	}
	if initEph > ephBytes {
		ephBytes = initEph
	}
	for name, value := range initOthers {
		if value > others[name] {
			others[name] = value
		}
	}

	if pod.Spec.Overhead != nil {
		cpuMilli += requestValue(pod.Spec.Overhead, v1.ResourceCPU)
		memBytes += requestValue(pod.Spec.Overhead, v1.ResourceMemory)
		ephBytes += requestValue(pod.Spec.Overhead, v1.ResourceEphemeralStorage)
		addOtherRequests(others, pod.Spec.Overhead)
	}

	return
}

func addOtherRequests(target map[v1.ResourceName]int64, requests v1.ResourceList) {
	for name, quantity := range requests {
		if isBuiltInResource(name) {
			continue
		}
		target[name] += quantity.Value()
	}
}

func requestValue(requests v1.ResourceList, resourceName v1.ResourceName) int64 {
	quantity, ok := requests[resourceName]
	if !ok {
		return 0
	}
	if resourceName == v1.ResourceCPU {
		return quantity.MilliValue()
	}
	return quantity.Value()
}

func isBuiltInResource(name v1.ResourceName) bool {
	return name == v1.ResourceCPU ||
		name == v1.ResourceMemory ||
		name == v1.ResourceEphemeralStorage ||
		name == v1.ResourcePods
}

func isPlaceholderPod(pod *v1.Pod) bool {
	return pod.Labels["component"] == "user-placeholder"
}
