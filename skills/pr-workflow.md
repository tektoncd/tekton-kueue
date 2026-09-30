---
name: pr-workflow
description: >
  Use when opening, monitoring, or iterating on a pull request in tekton-kueue,
  including PR body template, CI interpretation, and commit conventions.
---

# PR Workflow

Full lifecycle reference for pull requests in the tekton-kueue repository.

## Overview

tekton-kueue is a Go controller (kubebuilder) integrating Tekton PipelineRuns with Kueue for resource-aware scheduling via admission webhook and CEL-based mutations. PRs target `main` on the upstream repository.

## When to Use

- About to open a PR or write a PR description
- Iterating on a PR based on review feedback
- Preparing commits for a change

## Branch Setup

If a dedicated branch already exists for this work, use it. Otherwise, create a branch from the latest main of `tektoncd/tekton-kueue`.

First, find which remote points to `tektoncd/tekton-kueue`:

```bash
git remote -v | grep tektoncd/tekton-kueue
```

Then fetch and branch from it:

```bash
git fetch <remote>
git checkout -b <branch-name> <remote>/main --no-track
```

Common setups: fork-based workflows typically name it `upstream`, while direct clones use `origin`.

Never branch from an old or diverged main.

## PR Body Template

The repo has a PR template at `.github/PULL_REQUEST_TEMPLATE.md`. Follow it:

```markdown
## What

<!-- Brief description of the change -->

## Why

<!-- Motivation or link to Jira issue, e.g. KFLUXINFRA-1234 -->

## Verification

- [ ] `make lint` passes
- [ ] `make test` passes
- [ ] Tested with `tekton-kueue mutate` CLI (if CEL/webhook changes)
- [ ] `make manifests` and `make generate` run cleanly (if API/RBAC changes)
```

**Rules:**
- **What** — Concise change list. Don't explain why here.
- **Why** — Motivation and/or Jira link. Keep it brief.
- **Verification** — Check off each item that applies. Add additional items if the change warrants it (e.g., e2e tests).

## PR Title

Prefix with the Jira key, keep it short:

```
KFLUXINFRA-1234: short description of the change
```

## Commit Conventions

Prefix every commit with the Jira key:

```
KFLUXINFRA-1234: short description of the change
```

Always use `-s` flag (DCO sign-off).

Trailers (at end of commit message body):
- Interactive sessions (human + agent): `Assisted-by: Agent Name`
- Agentic workflow (autonomous): `Authored-by: Agent Name`

## Key CI Checks

| Check | What It Does |
|-------|--------------|
| **test** | Go tests with envtest, coverage uploaded to Codecov. |
| **test-e2e** | Kind cluster with Kueue+Tekton, instrumented image, e2e tests, coverage via coverport-cli. |
| **lint** | golangci-lint on PRs. |
| **auto-merge** | Merges approved dependency PRs when all checks pass. |
| **Tekton pipelines** | Multi-arch container builds and security scans. Run inside Konflux — best investigated manually through the Konflux UI. |

**CI caveats:**
- E2E tests can be flaky due to Kind cluster setup — if logs show no relevant errors, rerun with `gh run rerun <run-id> --failed`.
- Tekton pipeline failures are best investigated manually through the Konflux UI. Use `/retest` to re-trigger, but don't try to diagnose these yourself.
- After modifying API types or RBAC markers, run `make manifests` and `make generate` before committing.

## Interactive Sessions

In interactive sessions (human + agent), always confirm with the human before pushing and opening the PR. Show them the commit message, PR title, and PR body for approval first. Never push or create a PR without explicit approval.

## Common Mistakes

| Mistake | Fix |
|---------|-----|
| Not running tests before pushing | Run `make test` and `make lint` locally, mention results in Testing |
| Putting explanation in Testing instead of evidence | Testing = which tests ran and passed. Why = explanation. |
| Branching from a stale main | Always fetch and reset from origin before branching |
| Missing Jira key in commit message | Prefix every commit with `KFLUXINFRA-1234` |
| Forgetting `make manifests` / `make generate` | Run after any API type or RBAC marker changes |
| Not testing CEL changes with `mutate` CLI | Use `tekton-kueue mutate` to preview webhook mutations offline before pushing |
| Running tests with `go test ./...` | Use `make test` — it handles envtest setup and code generation |
