package main

import (
	"os"

	"github.com/team-monolith-product/image-locality-filter/imagelocalityfilter"
	"k8s.io/component-base/cli"
	"k8s.io/kubernetes/cmd/kube-scheduler/app"
)

func main() {
	command := app.NewSchedulerCommand(
		app.WithPlugin(imagelocalityfilter.Name, imagelocalityfilter.New),
	)

	code := cli.Run(command)
	os.Exit(code)
}
