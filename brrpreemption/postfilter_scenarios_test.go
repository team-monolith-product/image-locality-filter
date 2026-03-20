package brrpreemption

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"testing"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/informers"
	clientsetfake "k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/events"
	"k8s.io/kubernetes/pkg/scheduler/framework"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/defaultbinder"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/feature"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/noderesources"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/queuesort"
	frameworkruntime "k8s.io/kubernetes/pkg/scheduler/framework/runtime"
	st "k8s.io/kubernetes/pkg/scheduler/testing"
)

var nodeResourcesFitFunc = frameworkruntime.FactoryAdapter(feature.Features{}, noderesources.NewFit)

func TestPostFilter_MostAllocatedWins(t *testing.T) {
	incoming := makePod("incoming", "", 1000, "500", nil)
	nodes := []*v1.Node{
		makeNode("node-a", "2000"),
		makeNode("node-b", "2000"),
	}
	pods := []*v1.Pod{
		makePod("a-user", "node-a", 100, "1200", nil),
		makePod("a-placeholder", "node-a", -10, "500", map[string]string{"component": "user-placeholder"}),
		makePod("b-user", "node-b", 100, "900", nil),
		makePod("b-placeholder", "node-b", -10, "700", map[string]string{"component": "user-placeholder"}),
	}

	ctx, fwk, plugin := newFrameworkAndPlugin(t, incoming, pods, nodes, 4)
	result, status := runPostFilter(t, ctx, fwk, plugin, incoming, unschedulableStatuses(nodes))
	if !status.IsSuccess() {
		t.Fatalf("expected success status, got: %v", status)
	}
	if result == nil || result.NominatedNodeName != "node-a" {
		t.Fatalf("expected nominated node node-a, got: %#v", result)
	}
}

func TestPostFilter_BucketRoundRobinTopBucket10NodesBucket4(t *testing.T) {
	incoming := makePod("incoming", "", 1000, "500", nil)

	var nodes []*v1.Node
	var pods []*v1.Pod
	for i := 1; i <= 10; i++ {
		nodeName := fmt.Sprintf("node-%02d", i)
		nodes = append(nodes, makeNode(nodeName, "4000"))

		userMem := 1600 - (i * 100) // node-01=1500 ... node-10=600
		pods = append(pods,
			makePod(fmt.Sprintf("user-%02d", i), nodeName, 100, fmt.Sprintf("%d", userMem), nil),
			makePod(fmt.Sprintf("placeholder-%02d", i), nodeName, -10, "3000", map[string]string{"component": "user-placeholder"}),
		)
	}

	ctx, fwk, plugin := newFrameworkAndPlugin(t, incoming, pods, nodes, 4)
	statuses := unschedulableStatuses(nodes)

	counts := map[string]int{}
	for i := 0; i < 40; i++ {
		result, status := runPostFilter(t, ctx, fwk, plugin, incoming, statuses)
		if !status.IsSuccess() {
			t.Fatalf("cycle %d: expected success status, got: %v", i, status)
		}
		if result == nil || result.NominatedNodeName == "" {
			t.Fatalf("cycle %d: expected nominated node, got: %#v", i, result)
		}
		counts[result.NominatedNodeName]++
	}

	topNodes := []string{"node-01", "node-02", "node-03", "node-04"}
	for _, name := range topNodes {
		if counts[name] != 10 {
			t.Fatalf("expected %s to be selected 10 times, got %d, all=%v", name, counts[name], counts)
		}
	}

	for i := 5; i <= 10; i++ {
		name := fmt.Sprintf("node-%02d", i)
		if counts[name] != 0 {
			t.Fatalf("expected %s to be selected 0 times, got %d, all=%v", name, counts[name], counts)
		}
	}
}

func TestPostFilter_ProjectedUsageOverridesVictimCount(t *testing.T) {
	incoming := makePod("incoming", "", 1000, "900", nil)
	nodes := []*v1.Node{
		makeNode("node-a", "4000"),
		makeNode("node-b", "4000"),
	}
	pods := []*v1.Pod{
		makePod("a-user-1", "node-a", 200, "2000", nil),
		makePod("a-user-2", "node-a", 100, "1400", nil),
		makePod("a-placeholder", "node-a", -10, "1300", map[string]string{"component": "user-placeholder"}),
		makePod("b-user-1", "node-b", 200, "1900", nil),
		makePod("b-placeholder", "node-b", -10, "1300", map[string]string{"component": "user-placeholder"}),
	}

	ctx, fwk, plugin := newFrameworkAndPlugin(t, incoming, pods, nodes, 4)
	result, status := runPostFilter(t, ctx, fwk, plugin, incoming, unschedulableStatuses(nodes))
	if !status.IsSuccess() {
		t.Fatalf("expected success status, got: %v", status)
	}
	if result == nil || result.NominatedNodeName != "node-a" {
		t.Fatalf("expected nominated node node-a, got: %#v", result)
	}
}

func runPostFilter(
	t *testing.T,
	ctx context.Context,
	fwk framework.Framework,
	plugin *BucketedPreemption,
	pod *v1.Pod,
	statuses framework.NodeToStatusMap,
) (*framework.PostFilterResult, *framework.Status) {
	t.Helper()

	state := framework.NewCycleState()
	if _, status := fwk.RunPreFilterPlugins(ctx, state, pod); !status.IsSuccess() {
		t.Fatalf("unexpected PreFilter status: %v", status)
	}

	return plugin.PostFilter(ctx, state, pod, statuses)
}

func newFrameworkAndPlugin(
	t *testing.T,
	incoming *v1.Pod,
	pods []*v1.Pod,
	nodes []*v1.Node,
	bucketSize int32,
) (context.Context, framework.Framework, *BucketedPreemption) {
	t.Helper()

	cs := clientsetfake.NewSimpleClientset()
	informerFactory := informers.NewSharedInformerFactory(cs, 0)
	podInformer := informerFactory.Core().V1().Pods().Informer()
	if err := podInformer.GetStore().Add(incoming); err != nil {
		t.Fatalf("failed to add incoming pod: %v", err)
	}
	for i := range pods {
		if err := podInformer.GetStore().Add(pods[i]); err != nil {
			t.Fatalf("failed to add pod %s: %v", pods[i].Name, err)
		}
	}

	cs.PrependReactor("delete", "pods", func(action clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	fwk, err := st.NewFramework(
		[]st.RegisterPluginFunc{
			st.RegisterQueueSortPlugin(queuesort.Name, queuesort.New),
			st.RegisterPluginAsExtensions(noderesources.Name, nodeResourcesFitFunc, "Filter", "PreFilter"),
			st.RegisterBindPlugin(defaultbinder.Name, defaultbinder.New),
		},
		"",
		ctx.Done(),
		frameworkruntime.WithClientSet(cs),
		frameworkruntime.WithEventRecorder(&events.FakeRecorder{}),
		frameworkruntime.WithInformerFactory(informerFactory),
		frameworkruntime.WithPodNominator(&noopPodNominator{}),
		frameworkruntime.WithSnapshotSharedLister(newTestSharedLister(pods, nodes)),
	)
	if err != nil {
		t.Fatalf("failed to create framework: %v", err)
	}

	raw, _ := json.Marshal(BucketedPreemptionArgs{BucketSize: bucketSize})
	p, err := New(&runtime.Unknown{Raw: raw}, fwk)
	if err != nil {
		t.Fatalf("failed to create plugin: %v", err)
	}

	return ctx, fwk, p.(*BucketedPreemption)
}

func makeNode(name, memory string) *v1.Node {
	return st.MakeNode().Name(name).Capacity(map[v1.ResourceName]string{v1.ResourceMemory: memory}).Obj()
}

func makePod(name, nodeName string, priority int32, memory string, labels map[string]string) *v1.Pod {
	p := st.MakePod().
		Name(name).
		Namespace(v1.NamespaceDefault).
		UID(name).
		Priority(priority).
		Req(map[v1.ResourceName]string{v1.ResourceMemory: memory})
	if nodeName != "" {
		p = p.Node(nodeName)
	}
	if labels != nil {
		p = p.Labels(labels)
	}
	return p.Obj()
}

func unschedulableStatuses(nodes []*v1.Node) framework.NodeToStatusMap {
	m := make(framework.NodeToStatusMap, len(nodes))
	for _, node := range nodes {
		m[node.Name] = framework.NewStatus(framework.Unschedulable)
	}
	return m
}

type noopPodNominator struct{}

func (n *noopPodNominator) AddNominatedPod(pod *framework.PodInfo, nominatingInfo *framework.NominatingInfo) {
}

func (n *noopPodNominator) DeleteNominatedPodIfExists(pod *v1.Pod) {
}

func (n *noopPodNominator) UpdateNominatedPod(oldPod *v1.Pod, newPodInfo *framework.PodInfo) {
}

func (n *noopPodNominator) NominatedPodsForNode(nodeName string) []*framework.PodInfo {
	return nil
}

type testSharedLister struct {
	nodeInfoLister *testNodeInfoLister
}

func (s *testSharedLister) NodeInfos() framework.NodeInfoLister {
	return s.nodeInfoLister
}

func (s *testSharedLister) StorageInfos() framework.StorageInfoLister {
	return &testStorageInfoLister{}
}

type testStorageInfoLister struct{}

func (s *testStorageInfoLister) IsPVCUsedByPods(key string) bool {
	return false
}

type testNodeInfoLister struct {
	list []*framework.NodeInfo
	by   map[string]*framework.NodeInfo
}

func (l *testNodeInfoLister) List() ([]*framework.NodeInfo, error) {
	return l.list, nil
}

func (l *testNodeInfoLister) HavePodsWithAffinityList() ([]*framework.NodeInfo, error) {
	return nil, nil
}

func (l *testNodeInfoLister) HavePodsWithRequiredAntiAffinityList() ([]*framework.NodeInfo, error) {
	return nil, nil
}

func (l *testNodeInfoLister) Get(nodeName string) (*framework.NodeInfo, error) {
	if nodeInfo, ok := l.by[nodeName]; ok {
		return nodeInfo, nil
	}
	return nil, fmt.Errorf("node %q not found", nodeName)
}

func newTestSharedLister(pods []*v1.Pod, nodes []*v1.Node) framework.SharedLister {
	by := make(map[string]*framework.NodeInfo, len(nodes))
	list := make([]*framework.NodeInfo, 0, len(nodes))

	for _, node := range nodes {
		nodeInfo := framework.NewNodeInfo()
		nodeInfo.SetNode(node)
		by[node.Name] = nodeInfo
		list = append(list, nodeInfo)
	}

	for _, pod := range pods {
		nodeName := pod.Spec.NodeName
		if nodeName == "" {
			continue
		}
		nodeInfo, ok := by[nodeName]
		if !ok {
			continue
		}
		nodeInfo.AddPod(pod)
	}

	sort.Slice(list, func(i, j int) bool {
		return list[i].Node().Name < list[j].Node().Name
	})

	return &testSharedLister{
		nodeInfoLister: &testNodeInfoLister{
			list: list,
			by:   by,
		},
	}
}
