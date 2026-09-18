# Developing tekton-kueue

## Prerequisites

Install [Go](https://go.dev/doc/install) at the version declared in
[`go.mod`](go.mod), [Git](https://git-scm.com/), and `make`. A Kubernetes
cluster and [`kubectl`](https://kubernetes.io/docs/tasks/tools/) are required
only for deployment and end-to-end testing.

## Get the source

Fork this repository, then clone your fork and add the upstream repository:

```sh
git clone git@github.com:${GITHUB_USER}/tekton-kueue.git
cd tekton-kueue
git remote add upstream https://github.com/tektoncd/tekton-kueue.git
```

## Build and test

```sh
make build
make test
make lint
```

Run `make help` for the complete list of targets. End-to-end tests require an
isolated [Kind](https://kind.sigs.k8s.io/) cluster and run with:

```sh
make test-e2e
```

For local deployment and usage, follow the [README](README.md#getting-started).
