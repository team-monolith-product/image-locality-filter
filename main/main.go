package main

import (
	"os"

	"github.com/team-monolith-product/image-locality-filter/bucketedroundrobin"
	"github.com/team-monolith-product/image-locality-filter/imagelocalityfilter"
	"github.com/team-monolith-product/image-locality-filter/placeholderawarefit"
	"k8s.io/component-base/cli"
	"k8s.io/kubernetes/cmd/kube-scheduler/app"
)

func main() {
	command := app.NewSchedulerCommand(
		app.WithPlugin(imagelocalityfilter.Name, imagelocalityfilter.New),
		app.WithPlugin(bucketedroundrobin.Name, bucketedroundrobin.New),
		app.WithPlugin(placeholderawarefit.Name, placeholderawarefit.New),
	)

	code := cli.Run(command)
	os.Exit(code)
}
