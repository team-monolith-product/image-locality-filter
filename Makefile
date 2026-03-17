# Adopted From
# https://github.com/team-monolith-product/scheduler-plugins/blob/master/Makefile

COMMONENVVAR=GOOS=$(shell uname -s | tr A-Z a-z)
BUILDENVVAR=CGO_ENABLED=0

VERSION=v1.25.7

.PHONY: build.amd64
build.amd64: clean
	$(COMMONENVVAR) $(BUILDENVVAR) GOARCH=amd64 go build -ldflags '-X k8s.io/component-base/version.gitVersion=$(VERSION) -w' -o bin/kube-scheduler main/main.go

.PHONY: clean
clean:
	rm -rf ./bin
