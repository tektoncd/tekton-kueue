#!/usr/bin/env bash

set -euo pipefail

fail() {
  echo "release verification failed: $*" >&2
  exit 1
}

[[ $# == 5 ]] || fail "usage: $0 VERSION EXPECTED_COMMIT RELEASE_ID PUBLIC_KEY_FILE OUTPUT_DIR"
version=$1
commit=$2
release_id=$3
key=$4
output=$5
repository=${GITHUB_REPOSITORY:-tektoncd/tekton-kueue}
image_repository=ghcr.io/tektoncd/tekton-kueue
source_url=https://github.com/tektoncd/tekton-kueue

[[ $version =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || fail "invalid version"
[[ $commit =~ ^[0-9a-f]{40}$ ]] || fail "invalid commit"
[[ $release_id =~ ^[1-9][0-9]*$ ]] || fail "invalid release ID"
[[ $repository =~ ^[A-Za-z0-9][A-Za-z0-9-]*/[A-Za-z0-9_.-]+$ &&
   ${repository##*/} != . && ${repository##*/} != .. ]] || fail "invalid GitHub repository"
[[ -n ${GH_TOKEN:-} ]] || fail "GH_TOKEN is required"
[[ -f $key && -s $key && -r $key ]] || fail "a nonempty configured public key file is required"
[[ -n $output && ! -L $output ]] || fail "invalid output directory"
if [[ -e $output ]]; then
  [[ -d $output ]] || fail "output must be a directory"
  [[ -z $(find "$output" -mindepth 1 -print -quit) ]] || fail "output directory must be empty"
fi
for command in gh jq curl crane cosign openssl awk cmp; do
  command -v "$command" >/dev/null || fail "missing command: $command"
done
openssl pkey -pubin -in "$key" -noout >/dev/null 2>&1 || fail "invalid configured public key"
if command -v sha256sum >/dev/null; then
  checksum=(sha256sum)
else
  command -v shasum >/dev/null || fail "sha256sum or shasum is required"
  checksum=(shasum -a 256)
fi

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
gh api --method GET "repos/$repository/releases/$release_id" > "$work/release.json"
jq -e --arg version "$version" --arg commit "$commit" --arg id "$release_id" '
  .id == ($id | tonumber) and .tag_name == $version and .target_commitish == $commit
  and (.draft | type == "boolean") and (.assets | type == "array")
  and ([.assets[] | select(.name == "release.yaml" or .name == "release.notags.yaml" or .name == "SHA256SUMS")]
    | length == 3 and (map(.name) | unique | length == 3)
    and all(.[]; (.id | type == "number") and .id > 0 and .id == (.id | floor))
    and (map(.id) | unique | length == 3))
' "$work/release.json" >/dev/null || fail "release metadata does not match candidate"

for file in release.yaml release.notags.yaml SHA256SUMS; do
  asset_id=$(jq -r --arg name "$file" '.assets[] | select(.name == $name) | .id' "$work/release.json")
  gh api --method GET -H "Accept: application/octet-stream" \
    "repos/$repository/releases/assets/$asset_id" > "$work/$file"
  [[ -s $work/$file ]] || fail "empty asset: $file"
done

# Parse before hashing: never pass downloaded filenames to a checksum tool.
declare -A seen=()
while IFS= read -r line || [[ -n $line ]]; do
  [[ $line =~ ^([0-9a-f]{64})[[:space:]][\ \*](release\.yaml|release\.notags\.yaml)$ ]] ||
    fail "unsafe or malformed SHA256SUMS"
  hash=${BASH_REMATCH[1]}
  file=${BASH_REMATCH[2]}
  [[ -z ${seen[$file]:-} ]] || fail "duplicate checksum filename"
  seen[$file]=1
  actual=$("${checksum[@]}" "$work/$file")
  [[ ${actual%% *} == "$hash" ]] || fail "checksum mismatch: $file"
done < "$work/SHA256SUMS"
[[ ${#seen[@]} == 2 ]] || fail "SHA256SUMS must contain exactly the two manifests"
for file in release.yaml release.notags.yaml SHA256SUMS; do
  curl --fail --silent --show-error --location \
    "https://infra.tekton.dev/tekton-releases/tekton-kueue/previous/$version/$file" \
    --output "$work/bucket-$file"
  cmp -s "$work/$file" "$work/bucket-$file" || fail "bucket mismatch: $file"
done
echo "PASS release metadata, checksums and bucket copies"

# Kustomize emits block YAML with top-level metadata and scalar image fields.
# Require the controller and webhook deployments, not just an image somewhere.
for file in release.yaml release.notags.yaml; do
  awk '
    /^---/ { kind = ""; name = "" }
    /^kind: / { kind = $2 }
    /^metadata:/ { metadata = 1; next }
    /^[^ ]/ { metadata = 0 }
    metadata && /^  name: / { name = $2 }
    $1 == "image:" { print kind "\t" name "\t" $2 }
  ' "$work/$file" > "$work/images"
  count=0
  controller=0
  webhook=0
  while IFS=$'\t' read -r kind name reference; do
    [[ $kind == Deployment ]] || fail "unexpected image outside a Deployment"
    case "$name" in
      tekton-kueue-controller-manager) controller=$((controller + 1)) ;;
      tekton-kueue-webhook) webhook=$((webhook + 1)) ;;
      *) fail "unexpected image deployment" ;;
    esac
    # Both variants must have the same digest; only release.yaml may have the version tag.
    prefix=$image_repository
    if [[ $file == release.yaml ]]; then
      prefix="$prefix:$version"
    fi
    [[ $reference == "$prefix"@sha256:* ]] || fail "unexpected image repository or tag"
    candidate=${reference##*@}
    [[ $candidate =~ ^sha256:[0-9a-f]{64}$ ]] || fail "invalid image digest"
    [[ ${digest:-$candidate} == "$candidate" ]] || fail "inconsistent manifest image digests"
    digest=$candidate
    count=$((count + 1))
  done < "$work/images"
  [[ $count == 2 && $controller == 1 && $webhook == 1 ]] ||
    fail "both controller and webhook images are required"
done
image="$image_repository@$digest"

crane manifest "$image" > "$work/index.json"
jq -e '
  [.manifests[].platform | .os + "/" + .architecture] | sort ==
  ["linux/amd64", "linux/arm64", "linux/ppc64le", "linux/s390x"]
' "$work/index.json" >/dev/null || fail "unexpected image platforms"
for platform in linux/amd64 linux/arm64 linux/ppc64le linux/s390x; do
  crane config --platform "$platform" "$image" > "$work/config.json"
  jq -e --arg source "$source_url" --arg platform "$platform" '
    (.os + "/" + .architecture) == $platform
    and .config.Labels["org.opencontainers.image.source"] == $source
    and .config.Labels["org.opencontainers.image.url"] == $source
  ' "$work/config.json" >/dev/null || fail "incorrect OCI labels or platform: $platform"
done
echo "PASS manifest images, four platforms and OCI labels"

cosign verify --key "$key" "$image" > "$work/signatures.json"
jq -e -s --arg digest "$digest" '
  [ .[] | if type == "array" then .[] else . end
    | select(.critical.image["docker-manifest-digest"] == $digest) ] | length > 0
' \
  "$work/signatures.json" >/dev/null || fail "no verified image signature"
cosign verify-attestation --key "$key" --type https://slsa.dev/provenance/v1 \
  "$image" > "$work/attestations.json"
# cosign emits verified DSSE envelopes as JSON objects (or an array).
jq -e -s --arg digest "${digest#sha256:}" --arg commit "$commit" '
  def repository:
    # Chains uses git+https URIs with an optional ref suffix.
    test("^(git\\+)?https://github\\.com/tektoncd/tekton-kueue(\\.git)?(@[^[:space:]]+)?$");
  def source:
    (.uri // "" | repository)
    and ((.digest.sha1 // .digest.gitCommit) == $commit);
  [ .[] | if type == "array" then .[] else . end
    | .payload as $payload | $payload | @base64d | fromjson
    | select(._type == "https://in-toto.io/Statement/v1"
      and .predicateType == "https://slsa.dev/provenance/v1")
    | select(any(.subject[]; .digest.sha256 == $digest))
    | select(
      any(.predicate.buildDefinition.resolvedDependencies[]?; source)
      or (.predicate.buildDefinition.externalParameters.buildConfigSource
        | (.repository // "" | repository) and .ref == ("sha1:" + $commit)))
    | $payload
  ] | if length > 0 then . else error("no matching verified provenance") end
' "$work/attestations.json" > "$work/verified-payloads.json" ||
  fail "no verified provenance matching source and digest"
echo "PASS configured-key signature and SLSA provenance"

jq -n --argjson release_id "$release_id" --arg version "$version" --arg commit "$commit" \
  --arg image "$image" --arg digest "$digest" --slurpfile release "$work/release.json" \
  '{release_id: $release_id, version: $version, commit: $commit, image: $image,
    digest: $digest, draft: $release[0].draft,
    assets: [$release[0].assets[]
      | select(.name == "release.yaml" or .name == "release.notags.yaml" or .name == "SHA256SUMS")
      | {name, id}] | sort_by(.name)}' > "$work/release-info.json"
mkdir -p "$output"
for file in release.yaml release.notags.yaml SHA256SUMS release-info.json verified-payloads.json; do
  # Refuse to overwrite even if another process populated the output during verification.
  (set -o noclobber; cat "$work/$file" > "$output/$file")
done
echo "PASS $version artifacts saved to $output"
