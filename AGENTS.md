# tekton-kueue

Go controller (kubebuilder) integrating Tekton PipelineRuns with Kueue for
resource-aware scheduling via admission webhook and CEL-based mutations.

## Quick Commands

| Action         | Command                                               |
|----------------|-------------------------------------------------------|
| Build          | `make build`                                          |
| Unit tests     | `make test`                                           |
| E2E tests      | `make test-e2e` (requires Kind cluster)               |
| Lint           | `make lint`                                           |
| Generate CRDs  | `make manifests`                                      |
| Generate code  | `make generate`                                       |
| Deploy         | `make deploy IMG=<tag>`                               |
| Mutate CLI     | `tekton-kueue mutate --pipelinerun-file <f> --config-dir <d>` |

### Single-File Verification

- Lint package: `golangci-lint run ./path/to/package/`
- Vet package: `go vet ./path/to/package/`
- Test package: `go test ./path/to/package/`
- Test with race: `go test -race ./path/to/package/`
- Format file: `gofmt -w path/to/file.go`

## Project Layout

- `cmd/` — entrypoint with subcommands: `controller`, `webhook`, `mutate`.
- `internal/controller/` — PipelineRun → Workload reconciler.
- `internal/webhook/` — admission webhook with CEL mutation engine.
- `internal/cel/` — CEL functions: `annotation()`, `label()`, `priority()`,
  `resource()`, `managedBy()`, `multiKueue()` plus convenience vars
  (`plrNamespace`, `pacEventType`).
- `pkg/` — `mutate` (CLI logic), `common` (utilities), `config` (ConfigMap).
- `config/` — Kustomize manifests; `test/e2e/` — e2e tests.

## Key Conventions

- Kubebuilder scaffolded — run `make manifests` and `make generate` after
  modifying API types or RBAC markers.
- CEL `resource()` values with same key are summed; rejects negatives.
- MultiKueue routing via CEL: `multiKueue()` or `multiKueue(queueName)` in
  expressions. `managedBy(value)` for custom controller routing. The boolean
  `multiKueueOverride: true` still works for backward compatibility.
- `mutate` CLI previews webhook behavior offline — use before production.
- Coverage instrumentation toggled via `ENABLE_COVERAGE=true`.
- All changes via PR; review required.

## Testing

- **Unit tests**: Ginkgo with envtest. Run with `make test`. Covers
  controller, webhook, and CEL engine.
- **E2E tests**: Kind cluster with Kueue and Tekton. `make test-e2e`
  handles full lifecycle. Coverage via coverport-cli, uploaded to Codecov.
- **CLI testing**: `tekton-kueue mutate` previews webhook mutations
  offline — validates CEL expressions without a cluster.

## CI Pipeline (GitHub Actions)

- `test` — unit tests on push/PR, coverage to Codecov.
- `test-e2e` — Kind cluster with Kueue+Tekton, instrumented image, e2e
  tests, coverage via coverport-cli.
- `lint` — golangci-lint on PRs.
- `dep-triage` — auto-triages Renovate/Konflux bot dependency PRs.
- `auto-merge` — merges approved dependency PRs when all checks pass.

## Pattern References

When making common changes, use these as reference implementations:
- **New CEL function**: see `internal/cel/compiler.go` (`createCELEnvironment` registers functions)
- **New webhook mutation**: see `internal/webhook/v1/pipelinerun_webhook.go`
- **New controller reconciler**: see `internal/controller/pipelinerun_controller.go`
- **New CLI subcommand**: see `cmd/main.go` for dispatch and flag parsing, `pkg/mutate/` for logic
- **Dependency management policy**: see `docs/dependency-policy.md`

## Gotchas

- E2E tests use `KIND_EXPERIMENTAL_PROVIDER=podman`.
- CertManager required for webhook TLS certificates.
- Kueue must have `pipelineruns.tekton.dev` in external frameworks config.
- Changing CEL expression syntax may silently change PipelineRun mutations
  in production — always test with `mutate` CLI first.

## Skills

- Before opening a PR, writing a PR description, or interpreting CI results, read `skills/pr-workflow.md`
- When a CI check fails on a PR, read `skills/ci-troubleshooting.md`
- When working interactively on new features or significant changes, read `skills/brainstorming-workflow.md` before making changes
