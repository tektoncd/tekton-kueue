---
name: ci-troubleshooting
description: >
  Use when a CI check fails on a PR in tekton-kueue and you need to
  understand what failed, how to read the logs, and how to fix it.
---

# CI Troubleshooting

## Overview

How to investigate and fix CI failures on tekton-kueue PRs.

## When to Use

- A CI check failed on your PR
- You need to understand what a CI comment or status means
- You want to re-trigger a flaky test

## Prerequisites

Verify `gh` CLI is installed and authenticated:

```bash
gh auth status
```

All CI investigation commands below depend on it.

## Reading CI Logs

### GitHub Actions checks

```bash
gh pr checks <PR-number> --repo tektoncd/tekton-kueue
```

To investigate a failed check:

```bash
gh run view <run-id> --repo tektoncd/tekton-kueue
gh run view <run-id> --repo tektoncd/tekton-kueue --log-failed
```

### Tekton / Konflux pipeline checks

Tekton pipeline runs (pull-request and push pipelines defined in `.tekton/`) execute inside the Konflux platform. These are best investigated manually through the Konflux UI.

If a Tekton pipeline check fails, you can comment `/retest` on the PR to re-trigger, but if the failure persists, escalate to a human who can inspect the logs in the Konflux UI.

## Common Failures

### test (unit tests)

Ginkgo tests with envtest, run via `go test`. The GitHub Actions log shows which specs failed and the failure messages. To reproduce locally:

```bash
make test
```

If a specific test is failing, run it in isolation:

```bash
go test ./path/to/package/ -run "TestName"
```

For Ginkgo specs, you can also focus by description:

```bash
go test ./path/to/package/ --ginkgo.focus="description of failing spec"
```

### test-e2e

End-to-end tests running on a Kind cluster with Kueue and Tekton installed. These require Kind, Kueue, Tekton, and CertManager. Common causes of failure:
- Kind cluster not running or misconfigured
- Kueue or Tekton not installed in the cluster
- CertManager not deployed (required for webhook TLS)
- Actual test failure (check the Ginkgo output for the failing spec)

To reproduce locally:

```bash
# Set up a Kind cluster with dependencies (if not already running)
kind create cluster
make cert-manager
make tekton
make kueue

# Build and load the image, then run e2e tests
make load-image
make deploy
make test-e2e
```

Note: E2E tests use `KIND_EXPERIMENTAL_PROVIDER=podman` in CI.

### lint

golangci-lint violations. The log shows the exact file, line, and linter rule. Fix the reported issues or run locally:

```bash
make lint
```

### go mod tidy

`go.mod` or `go.sum` are not tidy. Fix:

```bash
go mod tidy
```

Then commit the changes.

### Tekton pipeline failures

The `.tekton/` pipelines run security scans and multi-arch container builds. These run inside Konflux and their logs are not accessible from the CLI. If they fail:

1. Comment `/retest` on the PR to re-trigger.
2. If the failure persists, these are best investigated manually through the Konflux UI by a human.

## Re-running Failed Jobs

Find the run ID from the PR checks output:

```bash
gh pr checks <PR-number> --repo tektoncd/tekton-kueue
```

The run ID is part of the check's detail URL. Then rerun only the failed jobs:

```bash
gh run rerun <run-id> --repo tektoncd/tekton-kueue --failed
```
