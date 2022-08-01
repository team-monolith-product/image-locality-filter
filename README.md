# image-locality-filter
A kubernetes scheduler plugin which filters nodes based on image locality.

## Purpose

Filtering nodes based on image locality is a extremely rare use case.
Because images are downloaded immediately after the pod is created,
so there is no need to filter nodes based on image locality.

However, downloading images need a few minutes to complete,
and it harms user experience.
So in some environment, daemonset is used to download images preemptively.
In this case, we should prevent pods to be scheduled on nodes without images.

## Installation

This repository is assumed to be used by Team Monolith,
so there are no way to install this plugin in easy way.

You must build image for your environment and upload it to docker registry.
Then, you can use the image in your kubernetes cluster.

```
git tag v{major}.{minor}.{patch}
make release-image
```

## Reference
https://github.com/jupyterhub/zero-to-jupyterhub-k8s/issues/1851