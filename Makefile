# Adopted From
# https://github.com/team-monolith-product/scheduler-plugins/blob/master/Makefile

COMMONENVVAR=GOOS=$(shell uname -s | tr A-Z a-z)
BUILDENVVAR=CGO_ENABLED=0

RELEASE_REGISTRY=631535140228.dkr.ecr.ap-northeast-2.amazonaws.com
RELEASE_VERSION?=v$(shell date +%Y%m%d)-$(shell git describe --tags --match "v*")
RELEASE_IMAGE=tmn-ecr-k8s-scheduler:$(RELEASE_VERSION)

# VERSION is the scheduler's version
#
# The RELEASE_VERSION variable can have one of two formats:
# v20201009-v0.18.800-46-g939c1c0 - automated build for a commit(not a tag) and also a local build
# v20200521-v0.18.800             - automated build for a tag
VERSION=$(shell echo $(RELEASE_VERSION) | awk -F - '{print $$2}')

.PHONY: build.amd64
build.amd64: clean
	$(COMMONENVVAR) $(BUILDENVVAR) GOARCH=amd64 go build -ldflags '-X k8s.io/component-base/version.gitVersion=$(VERSION) -w' -o bin/kube-scheduler main/main.go

.PHONY: release-image
release-image: clean
	aws ecr get-login-password --region ap-northeast-2 | docker login --username AWS --password-stdin 631535140228.dkr.ecr.ap-northeast-2.amazonaws.com
	docker build -f Dockerfile --build-arg ARCH="amd64" --build-arg RELEASE_VERSION="$(RELEASE_VERSION)" -t $(RELEASE_REGISTRY)/$(RELEASE_IMAGE) .
	docker push $(RELEASE_REGISTRY)/$(RELEASE_IMAGE)

.PHONY: clean
clean:
	rm -rf ./bin
