# Adopted From
# https://github.com/team-monolith-product/scheduler-plugins/blob/master/build/scheduler/Dockerfile

ARG ARCH
FROM golang:1.19

WORKDIR /go/src/github.com/team-monolith-product/image-locality-filter
COPY . .
ARG ARCH
ARG RELEASE_VERSION
RUN RELEASE_VERSION=${RELEASE_VERSION} make build.$ARCH

FROM $ARCH/alpine:3.16

COPY --from=0 /go/src/github.com/team-monolith-product/image-locality-filter/bin/kube-scheduler /usr/local/bin/kube-scheduler

WORKDIR /usr/local/bin
CMD ["kube-scheduler"]
