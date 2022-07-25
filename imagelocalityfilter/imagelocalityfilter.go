package imagelocalityfilter

import (
	"context"
	"strings"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/kubernetes/pkg/scheduler/framework"
)

type ImageLocalityFilter struct {
	handle framework.Handle
}

var _ framework.FilterPlugin = &ImageLocalityFilter{}

const Name = "ImageLocalityFilter"

func New(_ runtime.Object, h framework.Handle) (framework.Plugin, error) {
	return &ImageLocalityFilter{handle: h}, nil
}

func (pl *ImageLocalityFilter) Name() string {
	return Name
}

func (pl *ImageLocalityFilter) Filter(ctx context.Context, _ *framework.CycleState, pod *v1.Pod, nodeInfo *framework.NodeInfo) *framework.Status {
	node := nodeInfo.Node()
	if node == nil {
		return framework.NewStatus(framework.Error, "node not found")
	}

	for _, container := range pod.Spec.Containers {
		if _, ok := nodeInfo.ImageStates[normalizedImageName(container.Image)]; !ok {
			return framework.NewStatus(framework.Unschedulable, "image not found")
		}
	}

	return nil
}

// Adopted from https://github.com/kubernetes/kubernetes/blob/master/pkg/scheduler/framework/plugins/imagelocality/image_locality.go
func normalizedImageName(name string) string {
	if strings.LastIndex(name, ":") <= strings.LastIndex(name, "/") {
		name = name + ":latest"
	}
	return name
}
