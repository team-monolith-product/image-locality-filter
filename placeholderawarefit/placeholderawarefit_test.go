package placeholderawarefit

import (
	"context"
	"fmt"
	"testing"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/kubernetes/pkg/scheduler/framework"
)

func makeNode(name string, cpuMilli int64, memBytes int64, maxPods int64) *v1.Node {
	return &v1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: v1.NodeStatus{
			Allocatable: v1.ResourceList{
				v1.ResourceCPU:    *resource.NewMilliQuantity(cpuMilli, resource.DecimalSI),
				v1.ResourceMemory: *resource.NewQuantity(memBytes, resource.BinarySI),
				v1.ResourcePods:   *resource.NewQuantity(maxPods, resource.DecimalSI),
			},
		},
	}
}

func makePod(name string, cpuMilli int64, memBytes int64, labels map[string]string) *v1.Pod {
	return &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name: "main",
					Resources: v1.ResourceRequirements{
						Requests: v1.ResourceList{
							v1.ResourceCPU:    *resource.NewMilliQuantity(cpuMilli, resource.DecimalSI),
							v1.ResourceMemory: *resource.NewQuantity(memBytes, resource.BinarySI),
						},
					},
				},
			},
		},
	}
}

func TestFilterIgnoresPlaceholderForResourceFit(t *testing.T) {
	plugin := &PlaceholderAwareNodeResourcesFit{}
	node := makeNode("n1", 1000, 1000, 10)
	nodeInfo := framework.NewNodeInfo(
		makePod("placeholder", 900, 900, map[string]string{"component": "user-placeholder"}),
	)
	nodeInfo.SetNode(node)

	incoming := makePod("incoming", 200, 200, nil)
	status := plugin.Filter(context.Background(), nil, incoming, nodeInfo)
	if status != nil {
		t.Fatalf("expected pod to fit when only placeholder occupies node, got %v", status)
	}
}

func TestFilterRejectsWhenRealPodsExceedAllocatable(t *testing.T) {
	plugin := &PlaceholderAwareNodeResourcesFit{}
	node := makeNode("n1", 1000, 1000, 10)
	nodeInfo := framework.NewNodeInfo(
		makePod("real", 900, 900, nil),
	)
	nodeInfo.SetNode(node)

	incoming := makePod("incoming", 200, 200, nil)
	status := plugin.Filter(context.Background(), nil, incoming, nodeInfo)
	if status == nil || status.Code() != framework.Unschedulable {
		t.Fatalf("expected unschedulable due to real pod requests, got %v", status)
	}
}

func TestFilterCountsPlaceholderWhenIncomingIsPlaceholder(t *testing.T) {
	plugin := &PlaceholderAwareNodeResourcesFit{}
	node := makeNode("n1", 1000, 1000, 10)
	nodeInfo := framework.NewNodeInfo(
		makePod("placeholder-existing", 900, 900, map[string]string{"component": "user-placeholder"}),
	)
	nodeInfo.SetNode(node)

	incoming := makePod("placeholder-incoming", 200, 200, map[string]string{"component": "user-placeholder"})
	status := plugin.Filter(context.Background(), nil, incoming, nodeInfo)
	if status == nil || status.Code() != framework.Unschedulable {
		t.Fatalf("expected unschedulable for placeholder pod when placeholder load is high, got %v", status)
	}
}

func TestFilterIgnoresPlaceholderForPodCount(t *testing.T) {
	plugin := &PlaceholderAwareNodeResourcesFit{}
	node := makeNode("n1", 2000, 2000, 2)
	nodeInfo := framework.NewNodeInfo(
		makePod("real", 100, 100, nil),
		makePod("placeholder", 100, 100, map[string]string{"component": "user-placeholder"}),
	)
	nodeInfo.SetNode(node)

	incoming := makePod("incoming", 100, 100, nil)
	status := plugin.Filter(context.Background(), nil, incoming, nodeInfo)
	if status != nil {
		t.Fatalf("expected pod count fit when placeholder excluded, got %v", status)
	}
}

func TestFilterRejectsWhenRealPodCountIsFull(t *testing.T) {
	plugin := &PlaceholderAwareNodeResourcesFit{}
	node := makeNode("n1", 2000, 2000, 2)
	nodeInfo := framework.NewNodeInfo(
		makePod("real-1", 100, 100, nil),
		makePod("real-2", 100, 100, nil),
		makePod("placeholder", 100, 100, map[string]string{"component": "user-placeholder"}),
	)
	nodeInfo.SetNode(node)

	incoming := makePod("incoming", 100, 100, nil)
	status := plugin.Filter(context.Background(), nil, incoming, nodeInfo)
	if status == nil || status.Code() != framework.Unschedulable {
		t.Fatalf("expected unschedulable due to real pod count, got %v", status)
	}
}

func TestFilterCountsPlaceholderPodCountWhenIncomingIsPlaceholder(t *testing.T) {
	plugin := &PlaceholderAwareNodeResourcesFit{}
	node := makeNode("n1", 2000, 2000, 2)
	nodeInfo := framework.NewNodeInfo(
		makePod("real", 100, 100, nil),
		makePod("placeholder-existing", 100, 100, map[string]string{"component": "user-placeholder"}),
	)
	nodeInfo.SetNode(node)

	incoming := makePod("placeholder-incoming", 100, 100, map[string]string{"component": "user-placeholder"})
	status := plugin.Filter(context.Background(), nil, incoming, nodeInfo)
	if status == nil || status.Code() != framework.Unschedulable {
		t.Fatalf("expected unschedulable due to pod count for placeholder pod, got %v", status)
	}
}

// Reproduce the t3.large scenario observed in production:
// - t3.large: allocatable CPU 1930m, mem ~7.4Gi (7245952Ki = 7419854848 bytes)
// - 3 placeholder pods (250m CPU, 2Gi mem each) already on node
// - User pods (250m CPU, 2Gi mem each) scheduled sequentially
// - After each successful filter, simulate assume by calling NodeInfo.AddPod
// - Expect: filter should reject once real pod requests exceed allocatable
func TestSimulateT3LargeSequentialScheduling(t *testing.T) {
	plugin := &PlaceholderAwareNodeResourcesFit{}

	// t3.large allocatable
	allocCPU := int64(1930)                  // millicores
	allocMem := int64(7245952) * int64(1024) // Ki -> bytes

	node := makeNode("t3-large", allocCPU, allocMem, 35)

	// 3 placeholders already on node
	placeholders := []*v1.Pod{
		makePod("placeholder-0", 250, 2*1024*1024*1024, map[string]string{"component": "user-placeholder"}),
		makePod("placeholder-1", 250, 2*1024*1024*1024, map[string]string{"component": "user-placeholder"}),
		makePod("placeholder-2", 250, 2*1024*1024*1024, map[string]string{"component": "user-placeholder"}),
	}

	nodeInfo := framework.NewNodeInfo(placeholders[0], placeholders[1], placeholders[2])
	nodeInfo.SetNode(node)

	// Log initial state
	t.Logf("allocatable: CPU=%dm, Mem=%d bytes (%.2f Gi)", allocCPU, allocMem, float64(allocMem)/(1024*1024*1024))
	t.Logf("placeholders: 3 x (250m CPU, 2Gi mem)")
	t.Logf("nodeInfo.Pods count after placeholders: %d", len(nodeInfo.Pods))
	t.Logf("nodeInfo.Requested: CPU=%dm, Mem=%d bytes", nodeInfo.Requested.MilliCPU, nodeInfo.Requested.Memory)

	scheduled := 0
	for i := 0; i < 40; i++ {
		incoming := makePod(fmt.Sprintf("user-%d", i), 250, 2*1024*1024*1024, nil)

		// Clone nodeInfo as scheduler does per cycle (snapshot)
		snapshot := nodeInfo.Clone()

		status := plugin.Filter(context.Background(), nil, incoming, snapshot)
		if status != nil {
			t.Logf("user-%d REJECTED: %s (after %d scheduled)", i, status.Message(), scheduled)
			break
		}

		// Simulate assume: add pod to nodeInfo (like cache.AssumePod -> AddPod)
		nodeInfo.AddPod(incoming)
		scheduled++
		t.Logf("user-%d PASSED: nodeInfo.Pods=%d, Requested CPU=%dm Mem=%.2f Gi",
			i, len(nodeInfo.Pods), nodeInfo.Requested.MilliCPU,
			float64(nodeInfo.Requested.Memory)/(1024*1024*1024))
	}

	// With allocatable ~7.4Gi and user pod 2Gi each, max should be 3 user pods
	// (3 * 2Gi = 6Gi < 7.4Gi, 4 * 2Gi = 8Gi > 7.4Gi)
	maxExpected := int(allocMem / (2 * 1024 * 1024 * 1024))
	t.Logf("scheduled=%d, maxExpected=%d", scheduled, maxExpected)

	if scheduled > maxExpected {
		t.Errorf("over-scheduled: got %d user pods on t3.large (max expected %d)", scheduled, maxExpected)
	}
}

// Same test but WITHOUT assume simulation (no AddPod between filter calls).
// This simulates what happens if snapshot is never updated between scheduling cycles.
func TestSimulateT3LargeWithoutAssume(t *testing.T) {
	plugin := &PlaceholderAwareNodeResourcesFit{}

	allocCPU := int64(1930)
	allocMem := int64(7245952) * int64(1024)

	node := makeNode("t3-large", allocCPU, allocMem, 35)

	placeholders := []*v1.Pod{
		makePod("placeholder-0", 250, 2*1024*1024*1024, map[string]string{"component": "user-placeholder"}),
		makePod("placeholder-1", 250, 2*1024*1024*1024, map[string]string{"component": "user-placeholder"}),
		makePod("placeholder-2", 250, 2*1024*1024*1024, map[string]string{"component": "user-placeholder"}),
	}

	nodeInfo := framework.NewNodeInfo(placeholders[0], placeholders[1], placeholders[2])
	nodeInfo.SetNode(node)

	scheduled := 0
	for i := 0; i < 40; i++ {
		incoming := makePod(fmt.Sprintf("user-%d", i), 250, 2*1024*1024*1024, nil)

		// Use the same unchanged nodeInfo every time (no assume)
		snapshot := nodeInfo.Clone()

		status := plugin.Filter(context.Background(), nil, incoming, snapshot)
		if status != nil {
			t.Logf("user-%d REJECTED (no-assume): %s (after %d scheduled)", i, status.Message(), scheduled)
			break
		}
		scheduled++
	}

	t.Logf("scheduled without assume=%d", scheduled)

	if scheduled > 1 {
		// Without assume, every call sees the same state, so all should pass
		// This is expected - the question is whether it's 1 or unlimited
		t.Logf("NOTE: without assume, filter always sees same state -> all pass (scheduled=%d)", scheduled)
	}
}

func TestFilterReturnsErrorWhenNodeMissing(t *testing.T) {
	plugin := &PlaceholderAwareNodeResourcesFit{}
	nodeInfo := framework.NewNodeInfo()
	incoming := makePod("incoming", 100, 100, nil)

	status := plugin.Filter(context.Background(), nil, incoming, nodeInfo)
	if status == nil || status.Code() != framework.Error {
		t.Fatalf("expected error status when node is nil, got %v", status)
	}
}
