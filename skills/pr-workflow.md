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

The repo has a PR template at `.github/PULL_REQUEST_TEMPLATE.md`, which
follows the Tekton community standards. Read that file and fill it in rather
than inventing your own headings — it is the source of truth if it changes.
As of this writing it has three sections:

````markdown
# Changes

<!-- Describe your changes here- ideally you can get that description straight from your descriptive commit message(s)! -->

# Submitter Checklist

<!-- The checklist shipped in the template, linking to the Tekton standards -->

# Release Notes

```release-note
NONE
```
````

**Rules:**
- **Changes** — Concise change list, usually drawn from the commit messages.
  Explain the motivation here too; the template has no separate "Why" section.
- **Submitter Checklist** — Check off each item that genuinely applies, and
  say briefly why where it is not obvious (e.g. which docs changed). Leave
  unchecked what does not apply rather than ticking everything.
- **Release Notes** — Replace `NONE` with a user-facing note for API changes,
  bug fixes, or anything needing an upgrade notice. Keep `NONE` for internal
  changes. Include the string "action required" when users must act.

Mention the verification you actually ran (`make test`, `make lint`,
`make verify-release`) in the Changes section or a PR comment, and say plainly
which checks you did **not** run locally — `make test-e2e` needs a Kind
cluster and is often left to CI.

## PR Title

Use the same conventional-commit form as the commits, and keep it short:

```
build(deps): align kueue to 0.19.14 for RHBOK 1.5
```

## Commit Conventions

Use conventional-commit prefixes — `feat:`, `fix:`, `ci:`, `build(deps):`,
`test:`, `docs:`, `refactor:`. Do not add a Jira key; this repo does not use
one.

Keep the subject short and put the reasoning in the body: what changed, why,
and any constraint a reviewer would otherwise have to rediscover (an awkward
version pin, a `replace` directive, an interface that forced adaptation).

Always use `-s` flag (DCO sign-off). It is enforced, so a commit without it
will block the PR.

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
| Not running tests before pushing | Run `make test` and `make lint` locally, and state the results on the PR |
| Claiming verification you did not do | Say which checks you ran and which you skipped — `make test-e2e` needs Kind and is usually left to CI |
| Missing DCO sign-off | Commit with `-s`; it is enforced and will block the PR |
| Branching from a stale main | Always fetch and branch from the remote that points at `tektoncd/tekton-kueue` |
| Branching from the fork's main when it has diverged | The fork can be ahead of or behind upstream. Check with `git rev-list --left-right --count upstream/main...origin/main` before branching |
| Assuming an existing PR is still open | Check `gh pr view <n> --json state` first; a merged PR cannot be updated and needs a follow-up PR |
| Replaying contradictory history in one PR | Don't ship "bump to X" followed by "downgrade to Y". Squash into one commit describing the net change |
| Forgetting `make manifests` / `make generate` | Run after any API type or RBAC marker changes |
| Not testing CEL changes with `mutate` CLI | Use `tekton-kueue mutate` to preview webhook mutations offline before pushing |
| Running tests with `go test ./...` | Use `make test` — it handles envtest setup and code generation |
