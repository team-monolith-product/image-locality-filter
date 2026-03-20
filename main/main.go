package main

import (
	"os"

	"github.com/team-monolith-product/image-locality-filter/bucketedroundrobin"
	"github.com/team-monolith-product/image-locality-filter/imagelocalityfilter"
	"github.com/team-monolith-product/image-locality-filter/placeholderawarefit"
	"k8s.io/component-base/cli"
	"k8s.io/kubernetes/cmd/kube-scheduler/app"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/feature"
	"k8s.io/kubernetes/pkg/scheduler/framework/runtime"
)

func main() {
	command := app.NewSchedulerCommand(
		app.WithPlugin(imagelocalityfilter.Name, imagelocalityfilter.New),
		app.WithPlugin(bucketedroundrobin.Name, bucketedroundrobin.New),
		app.WithPlugin(placeholderawarefit.Name, runtime.FactoryAdapter(feature.Features{}, placeholderawarefit.New)),
	)

	code := cli.Run(command)
	os.Exit(code)
}
