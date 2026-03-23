// Package main 은 커스텀 kube-scheduler 바이너리의 진입점이다.
package main

import (
	"os"

	"github.com/team-monolith-product/image-locality-filter/brrpreemption"
	"github.com/team-monolith-product/image-locality-filter/bucketedroundrobin"
	"github.com/team-monolith-product/image-locality-filter/imagelocalityfilter"
	"k8s.io/component-base/cli"
	"k8s.io/kubernetes/cmd/kube-scheduler/app"
)

func main() {
	command := app.NewSchedulerCommand(
		app.WithPlugin(imagelocalityfilter.Name, imagelocalityfilter.New),
		app.WithPlugin(bucketedroundrobin.Name, bucketedroundrobin.New),
		app.WithPlugin(brrpreemption.Name, brrpreemption.New),
	)

	code := cli.Run(command)
	os.Exit(code)
}
