# Tekton Kueue release runbook

This runbook is for Tekton Kueue release operators. Release policy and support dates are recorded in [`releases.md`](../releases.md).

The automated steps below become operational only after the release Pipeline tracked by [#527](https://github.com/tektoncd/tekton-kueue/issues/527) is merged and its plumbing configuration is deployed to the Tekton OCI CI/CD cluster.

## Before the release

1. Confirm the target version, LTS designation, end-of-life date, and compatibility ranges with the maintainers.
2. Confirm all intended changes are merged and their release-note blocks are complete.
3. From a clean checkout of the candidate commit, run generation, lint, unit, and end-to-end checks.
4. Run a release candidate with `releaseAsLatest=false` through the Tekton-controlled release Pipeline so it cannot update the `latest` image tag or artifacts.
5. Verify both generated manifests reference only `ghcr.io/tektoncd/tekton-kueue` images by immutable digest.
6. Install and upgrade the candidate on the supported Kubernetes, Tekton Pipelines, and Kueue versions.

## Publish an initial release

1. Create the policy-defined release branch from the verified commit, using the branch convention selected in [#20](https://github.com/tektoncd/tekton-kueue/issues/20).
2. Creating the release branch in `tektoncd/tekton-kueue` triggers Pipelines as Code in the Tekton OCI CI/CD cluster to start the release Pipeline from the trusted default-branch `.tekton/release.yaml`.
3. Monitor the PipelineRun in the `releases-tekton-kueue` namespace.
4. Review the generated GitHub release notes and verify all publication checks below before publishing the draft.

## Publish a patch release

1. Cherry-pick an approved fix to the supported release branch.
2. After the cherry-pick passes CI on the release branch, run the **Patch Release** GitHub workflow with the branch, next `vX.Y.Z` version, and whether it should update `latest`.
3. Monitor and verify the release as for an initial release.

The scheduled patch workflow may detect unreleased commits, but a release operator must still verify the resulting release before announcing it.

## Verify publication

Verify and record:

- the source tag and release branch;
- the GitHub release and attached `release.yaml`, `release.notags.yaml`, and `SHA256SUMS`;
- the object-storage copies under `https://infra.tekton.dev/tekton-releases/tekton-kueue/`;
- the controller image digest and supported architectures;
- the Tekton Chains signature and Rekor provenance entry; and
- successful clean installation and upgrade testing.

Do not announce the release until every promised artifact is downloadable and independently verifiable.

## After the release

1. Update [`releases.md`](../releases.md) with the release date, exact end-of-life date, compatibility ranges, and release links.
2. Close the `vX.Y` milestone if one was used.
3. Announce the release through the normal Tekton channels.

See the [Tekton signing documentation](https://github.com/tektoncd/plumbing/blob/main/docs/signing.md) for signature and provenance verification.
