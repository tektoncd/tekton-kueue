# Release staging and finalization

Releases have two separate stages. The release pipeline builds and signs a
versioned candidate, uploads its manifests under `previous/<version>/`, and
creates a GitHub draft. It does not update the image's `latest` tag or the
bucket's `latest/` manifests.

The **Finalize Release** workflow verifies that candidate, installs its exact
published image in a disposable cluster, and then publishes it. Updating
`latest` is an explicit choice in this workflow, after maintainer approval.
It never builds another image.

## Required environment

Provision `kueue-release-finalization` using the standalone module
in `tektoncd/plumbing` under `terraform/kueue-release-finalization/`. The environment
requires review by `tekton-kueue-maintainers`, disallows self-review and
administrator bypass, and allows only the protected workflow on `main`.
Merging the workflow alone does not configure these protections. The
finalizer also checks the environment's actual protection before mutations.

Set the environment variable `RELEASE_SIGNING_PUBLIC_KEY` through the required
Terraform signing-key input. Obtain the key from independently managed
release infrastructure and confirm its fingerprint with the release
maintainers. Do not bootstrap trust from the candidate's signature or Rekor
entry. The historical `tektoncd/chains/tekton.pub` does not match the key used
by the OCI `v0.4.3` release; it is not a fallback.

The workflow uses these existing credential names. Environment-scoped secrets
can override organization secrets where the existing credential is unsuitable:

| Name | Required access |
| --- | --- |
| `CHATOPS_TOKEN` | `tekton-robot` GitHub credential with repository contents write access, including draft releases |
| `GHCR_TOKEN` | `tekton-robot` package credential that can promote this repository's image |
| `OCI_API_KEY`, `OCI_FINGERPRINT`, `OCI_TENANCY_OCID`, `OCI_USER_OCID`, `OCI_REGION` | Existing release-storage identity, authorized to read the namespace and write `tekton-kueue/latest/` |

The finalizer verifies token ownership through GitHub, rather than assuming a
secret name proves its owner. Missing or incorrect credentials stop the run;
there is no personal-account fallback. Registry and storage credentials are
needed only when `promote_latest` is selected. Temporary OCI and Docker
credential files are removed on exit.

For key rotation, independently authenticate the replacement key, update the
managed environment variable, and test an existing candidate before allowing
publication. Keep historical public keys available for verifying old releases;
do not silently trust whatever key a candidate supplies.

## Approving a candidate

Run **Finalize Release** from `main`. Supply:

| Input | Meaning |
| --- | --- |
| `operation` | `finalize` to publish, or `repair` to correct only the attestation notes |
| `version` | The existing candidate's `vX.Y.Z` version |
| `source_commit` | Its full, approved source commit |
| `release_id` | The existing GitHub release's numeric ID |
| `image_digest` | The exact approved `sha256:` digest |
| `promote_latest` | Also update the image, bucket and GitHub latest release; default is false |

Check the source commit and digest before approving the environment deployment.
The workflow runs only the trusted `main` scripts; it does not check out or
execute scripts supplied by the candidate branch.

Before any promotion or publication, the finalizer:

1. Downloads the three release assets by ID, checks both manifest hashes, and
   compares the assets byte-for-byte with the versioned bucket objects.
2. Confirms that the controller and webhook reference the same expected image
   digest, that all four supported Linux platforms exist, and that their OCI
   repository labels are correct.
3. Verifies the image signature and SLSA v1 provenance against the configured
   key, including the provenance's source commit and subject digest.
4. For `finalize`, installs the finished manifests with supported pinned
   dependencies in a uniquely named Kind cluster. It checks controller and
   webhook readiness, resource mutation, Kueue admission, and PipelineRun
   completion. The cluster and its private kubeconfig are cleaned up on every
   exit; diagnostics are retained as workflow artifacts.
5. Checks that the release's tag, source, draft state and asset IDs have not
   changed since verification.
6. Corrects the attestation section using the Rekor entry whose payload hash
   matches the verified statement.

For `finalize`, optional latest promotion happens only after these gates.
The finalizer refuses to promote an older version over the current latest,
checks the resulting image digest and bucket contents, and publishes the
existing GitHub release with an explicit tag and source commit.

## Repair and retries

`repair` performs artifact and signature verification but does not install a
cluster, publish a draft, or promote image/bucket latest. It preserves the
release's publication state and GitHub latest selection while replacing only
the attestation section. `promote_latest` must be false for this operation.

A failed verification never falls back to an image-signature entry, an
untrusted key, or another account. A promotion can fail after an approved
image or object has already moved; the GitHub release is not published until
all requested promotion checks pass. Rerun with the same approved candidate
to complete that promotion without rebuilding it.

Do not delete versioned artifacts to work around a partial release. Preserve
them and their original source/signing evidence for recovery.

## Developer checks

```sh
bash hack/verify-release-artifacts_test.sh
bash hack/smoke-release_test.sh
bash hack/finalize-release_test.sh
```

These deterministic boundary tests do not mutate GitHub, the registry or
storage, and do not use the developer's current cluster. Real smoke QA uses
`hack/smoke-release.sh` with an already verified manifest and canonical image
digest; it creates and deletes only its own Kind cluster. The finalization
workflow pins its tool versions, including Kind, rather than relying on older
workstation tools.

The Patch Release workflow no longer accepts `release_as_latest`, and the
release pipeline no longer accepts `releaseAsLatest`. Select
`promote_latest` during finalization instead. The image-publishing Task keeps
the legacy parameter only to accept `false` from older supported branches;
passing `true` is rejected.
