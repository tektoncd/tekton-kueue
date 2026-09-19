# Tekton Kueue Releases

## Release cadence and support

Tekton Kueue follows the [Tekton release policy](https://github.com/tektoncd/community/blob/main/releases.md) and uses [semantic versioning](https://semver.org/).

A minor release is planned every three months. Each minor release is designated for long-term support (LTS) until the third subsequent minor release is published, which is approximately one year. During that period, patch releases may be issued for CVEs, dependency issues, and critical component issues covered by the Tekton release policy.

The first Tekton Kueue release covered by this policy is planned to be `v0.5.0`. Earlier releases remain available as historical releases but are not covered by this support commitment. Release branch naming will follow the resolution of [#20](https://github.com/tektoncd/tekton-kueue/issues/20) before `v0.5.0` is published.

## Proposed release artifacts

The Tekton-controlled release path is being implemented to publish:

- an immutable `vX.Y.Z` source tag and GitHub release notes;
- `release.yaml` and `release.notags.yaml` installation manifests;
- `SHA256SUMS` for the installation manifests;
- a multi-architecture controller image under `ghcr.io/tektoncd/tekton-kueue`, referenced by digest from the manifests; and
- image signatures and build provenance produced by Tekton Chains.

Once the release path is deployed, manifests will be stored under `https://infra.tekton.dev/tekton-releases/tekton-kueue/`. See the [release runbook](./tekton/release-cheat-sheet.md) for the operator procedure.

## Planned supported releases

### v0.5 (LTS)

- **Initial release:** `v0.5.0` (planned)
- **End of life:** when `v0.8.0` is published
- **Release branch:** selected after the branch convention in [#20](https://github.com/tektoncd/tekton-kueue/issues/20) is resolved
- **Compatibility:** recorded after the release-candidate test matrix completes

The release date, exact end-of-life date, compatibility ranges, and patch-release links will be recorded when `v0.5.0` is published.

## Historical releases

[`v0.4.0`](https://github.com/tektoncd/tekton-kueue/releases/tag/v0.4.0) and earlier releases used the publication path retained from before the repository transfer. They demonstrate the historical artifact shape but were not produced through Tekton-controlled release infrastructure.
