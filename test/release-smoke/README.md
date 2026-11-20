# Finished-release smoke test

Run the published artifact verifier first, using an independently configured
signing public key. Pass its saved manifest and immutable image reference to:

```sh
bash hack/smoke-release.sh VERIFIED_MANIFEST EXPECTED_IMAGE ARTIFACT_DIR
```

The script requires Bash, Kind, kubectl, jq, OpenSSL and a working container
provider. It creates its own cluster and kubeconfig, installs the manifest
without rebuilding its image, checks webhook mutation and Kueue admission,
and waits for a PipelineRun to succeed. It saves diagnostics and deletes only
the cluster it created, including on failure. The artifact directory must be
empty.

The pull-request workflow exercises the existing public v0.4.3 manifest,
pinned by SHA-256 and image digest, on an isolated runner. This installation
check is not a signing-key trust bootstrap. The finalization workflow will
call the same script with the candidate that passed signature verification.

`release.yaml` in this directory is an offline parsing fixture for
`bash hack/smoke-release_test.sh`; it is not an installable release.
