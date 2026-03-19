package placeholderawarefit

import (
	"context"
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

func TestFilterReturnsErrorWhenNodeMissing(t *testing.T) {
	plugin := &PlaceholderAwareNodeResourcesFit{}
	nodeInfo := framework.NewNodeInfo()
	incoming := makePod("incoming", 100, 100, nil)

	status := plugin.Filter(context.Background(), nil, incoming, nodeInfo)
	if status == nil || status.Code() != framework.Error {
		t.Fatalf("expected error status when node is nil, got %v", status)
	}
}
