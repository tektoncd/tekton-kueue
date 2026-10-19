#!/usr/bin/env bash

set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
container_tool=${CONTAINER_TOOL:-docker}
platform=${RELEASE_TEST_PLATFORM:-linux/amd64}
go_mod=${GO_MOD_FILE:-"$root/go.mod"}

image_for_step() {
  local step=$1
  local file=$2

  awk -v step="$step" '
    $1 == "-" && $2 == "name:" && $3 == step { found = 1; next }
    found && $1 == "image:" { print $2; exit }
  ' "$file"
}

publish_image=$(image_for_step publish-image-and-manifests "$root/tekton/publish.yaml")
go_image=$(image_for_step test-and-build "$root/tekton/release-pipeline.yaml")

test -n "$publish_image"
test -n "$go_image"
grep -Fq "apk add --no-cache bash make" "$root/tekton/publish.yaml"

"$container_tool" run --rm --platform "$platform" \
  -v "$root:/workspace/source" \
  "$publish_image" sh -ec '
    apk add --no-cache bash make >/dev/null
    cd /workspace/source
    make release \
      KUSTOMIZE=/go/bin/kustomize \
      RELEASE_DIR=/tmp/release \
      RELEASE_IMAGE=ko://github.com/konflux-ci/tekton-kueue/cmd \
      VERSION=v0.0.0-test
    test -s /tmp/release/release-v0.0.0-test.yaml
  '

"$container_tool" run --rm --platform "$platform" \
  -e GOTOOLCHAIN=local \
  -v "$go_mod:/workspace/go.mod:ro" \
  -w /workspace \
  "$go_image" go list -m >/dev/null
