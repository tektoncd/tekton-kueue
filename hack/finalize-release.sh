#!/usr/bin/env bash
set -euo pipefail

fail() { echo "release finalization failed: $*" >&2; exit 1; }
robot() {
  [[ $(gh api user --jq .login) == tekton-robot ]] || fail "GH_TOKEN must belong to tekton-robot"
}

[[ $# == 8 ]] ||
  fail "usage: $0 finalize|repair VERSION COMMIT RELEASE_ID DIGEST PUBLIC_KEY_FILE true|false WORK_DIR"
operation=$1
version=$2
commit=$3
release_id=$4
digest=$5
key=$6
latest=$7
work=$8
repository=tektoncd/tekton-kueue
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
[[ $operation == finalize || $operation == repair ]] || fail "invalid operation"
[[ $version =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ &&
   $commit =~ ^[0-9a-f]{40}$ && $release_id =~ ^[1-9][0-9]*$ &&
   $digest =~ ^sha256:[0-9a-f]{64}$ ]] || fail "invalid candidate identity"
[[ $latest == true || $latest == false ]] || fail "invalid latest selection"
[[ $operation != repair || $latest == false ]] || fail "repair cannot promote latest"
[[ ${GITHUB_REPOSITORY:-} == "$repository" &&
   ${GITHUB_REF:-} == refs/heads/main ]] || fail "run the trusted main-branch workflow"
[[ -n ${GH_TOKEN:-} && -f $key && -s $key ]] || fail "bot token and trusted public key are required"
[[ -n $work && ! -L $work ]] || fail "invalid work directory"
if [[ -e $work ]]; then
  [[ -d $work && -z $(find "$work" -mindepth 1 -print -quit) ]] ||
    fail "work directory must be empty"
fi
mkdir -p "$work"
work=$(cd "$work" && pwd)
robot

# An environment name in a workflow does not itself configure review protection.
gh api "repos/$repository/environments/kueue-release-finalization" > "$work/environment.json"
jq -e '
  .can_admins_bypass == false and
  any(.protection_rules[]?;
    .type == "required_reviewers" and .prevent_self_review == true and
    any(.reviewers[]?; .type == "Team" and .reviewer.id == 18766447))
' "$work/environment.json" >/dev/null || fail "release approval environment is not protected"

current=$(gh api "repos/$repository/releases/latest" --jq .tag_name)
[[ $current =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || fail "cannot determine current latest release"
publish_latest=$latest
[[ $current != "$version" ]] || publish_latest=true
if [[ $latest == true ]]; then
  for name in GHCR_TOKEN OCI_API_KEY OCI_FINGERPRINT OCI_TENANCY_OCID OCI_USER_OCID OCI_REGION; do
    [[ -n ${!name:-} ]] || fail "$name is required for latest promotion"
  done
  [[ $(GH_TOKEN="$GHCR_TOKEN" gh api user --jq .login) == tekton-robot ]] ||
    fail "GHCR_TOKEN must belong to tekton-robot"
  IFS=. read -r major minor patch <<< "${version#v}"
  IFS=. read -r old_major old_minor old_patch <<< "${current#v}"
  (( 10#$major > 10#$old_major ||
    (10#$major == 10#$old_major && 10#$minor > 10#$old_minor) ||
    (10#$major == 10#$old_major && 10#$minor == 10#$old_minor && 10#$patch >= 10#$old_patch) )) ||
    fail "refusing to promote an older release as latest"
fi

bash "$root/hack/verify-release-artifacts.sh" "$version" "$commit" "$release_id" "$key" "$work/verified"
info="$work/verified/release-info.json"
jq -e --arg digest "$digest" '.digest == $digest' "$info" >/dev/null ||
  fail "verified digest differs from the approved candidate"
image=$(jq -r .image "$info")
if [[ $operation == finalize ]]; then
  bash "$root/hack/smoke-release.sh" "$work/verified/release.yaml" "$image" "$work/smoke"
fi

# Bind the release-note UUID to bytes from an already verified statement.
jq -r '.[]' "$work/verified/verified-payloads.json" |
  while IFS= read -r payload; do
    hash=$(printf '%s' "$payload" | base64 -d | openssl dgst -sha256 -r)
    printf '%s\n' "${hash%% *}"
  done > "$work/payload-hashes"
curl -fsS -H 'Content-Type: application/json' \
  -d "{\"hash\":\"$digest\"}" https://rekor.sigstore.dev/api/v1/index/retrieve > "$work/rekor-index.json"
uuid=""
while IFS= read -r candidate; do
  [[ $candidate =~ ^[0-9a-f]+$ ]] || fail "invalid Rekor entry ID"
  curl -fsS "https://rekor.sigstore.dev/api/v1/log/entries/$candidate" |
    jq -r '.[].body' | base64 -d > "$work/rekor-entry.json"
  kind=$(jq -r .kind "$work/rekor-entry.json")
  [[ $kind == dsse || $kind == intoto ]] || continue
  hash=$(jq -r '.spec.payloadHash.value // .spec.content.payloadHash.value // empty' "$work/rekor-entry.json")
  if [[ -n $hash ]] && grep -Fxq -- "$hash" "$work/payload-hashes"; then
    uuid=$candidate
    break
  fi
done < <(jq -er '.[]' "$work/rekor-index.json")
[[ -n $uuid ]] || fail "no Rekor entry matches the verified provenance"

gh api "repos/$repository/releases/$release_id" > "$work/current-release.json"
jq -e --arg version "$version" --arg commit "$commit" --slurpfile info "$info" '
  .id == $info[0].release_id and .tag_name == $version
  and .target_commitish == $commit and .draft == $info[0].draft
  and ([.assets[]
    | select(.name == "release.yaml" or .name == "release.notags.yaml" or .name == "SHA256SUMS")
    | {name, id}] | sort_by(.name)) == $info[0].assets
' "$work/current-release.json" >/dev/null || fail "release changed after verification"

cat > "$work/attestation.md" <<EOF
## Attestation

The Rekor UUID for this release is \`$uuid\`.

Obtain the provenance for the released image:
\`\`\`shell
IMAGE=$image
rekor-cli verify --uuid $uuid
cosign download attestation "\$IMAGE" | jq -r '.payload | @base64d' | jq .
\`\`\`

Check its image subjects against the release manifest:
\`\`\`shell
curl -fsSL https://infra.tekton.dev/tekton-releases/tekton-kueue/previous/$version/release.notags.yaml -o release.notags.yaml
cosign download attestation "\$IMAGE" | jq -r '.payload | @base64d | fromjson | .subject[] | .name + "@sha256:" + .digest.sha256' |
  while IFS= read -r image; do grep -Fq -- "\$image" release.notags.yaml || exit 1; done
\`\`\`

EOF
jq --rawfile section "$work/attestation.md" --arg version "$version" --arg commit "$commit" \
  --arg operation "$operation" --arg latest "$publish_latest" '
  .body as $body
  | ($body | index("## Attestation\n")) as $start
  | ($body | index("<!-- Any special upgrade notice")) as $end
  | {tag_name:$version,target_commitish:$commit,
     draft:(if $operation == "repair" then .draft else false end),
     body:(if $start == null then $body + "\n\n" + $section
       elif $end == null or $end <= $start
       then error("unsupported release-note structure")
       else $body[0:$start] + $section + $body[$end:] end)}
  | .make_latest = $latest
' "$work/current-release.json" > "$work/patch.json"

if [[ $latest == true ]]; then
  # Keep credentials out of the caller's Docker configuration and process args.
  export DOCKER_CONFIG="$work/docker"
  mkdir -p "$DOCKER_CONFIG" "$work/oci"
  trap 'rm -rf "$work/oci" "$work/docker"' EXIT
  chmod 700 "$work/oci"
  printf '%s\n' "$OCI_API_KEY" > "$work/oci/key.pem"
  chmod 600 "$work/oci/key.pem"
  for value in "$OCI_USER_OCID" "$OCI_TENANCY_OCID" "$OCI_FINGERPRINT" "$OCI_REGION"; do
    [[ $value != *$'\n'* && $value != *$'\r'* ]] || fail "invalid OCI configuration"
  done
  cat > "$work/oci/config" <<EOF
[DEFAULT]
user=$OCI_USER_OCID
tenancy=$OCI_TENANCY_OCID
fingerprint=$OCI_FINGERPRINT
region=$OCI_REGION
key_file=$work/oci/key.pem
EOF
  chmod 600 "$work/oci/config"
  namespace=$(oci --config-file "$work/oci/config" os ns get --query data --raw-output)
  printf '%s' "$GHCR_TOKEN" | crane auth login ghcr.io --username tekton-robot --password-stdin
  crane cp "$image" "ghcr.io/tektoncd/tekton-kueue:latest"
  [[ $(crane digest ghcr.io/tektoncd/tekton-kueue:latest) == "$digest" ]] ||
    fail "latest image does not match verified digest"
  for file in release.yaml release.notags.yaml SHA256SUMS; do
    oci --config-file "$work/oci/config" os object put --namespace-name "$namespace" \
      --bucket-name tekton-releases --name "tekton-kueue/latest/$file" \
      --file "$work/verified/$file" --force >/dev/null
    curl -fsSL "https://infra.tekton.dev/tekton-releases/tekton-kueue/latest/$file" \
      --output "$work/latest-$file"
    cmp -s "$work/verified/$file" "$work/latest-$file" || fail "latest bucket verification failed: $file"
  done
fi

robot
gh api --method PATCH "repos/$repository/releases/$release_id" --input "$work/patch.json" > "$work/result.json"
jq -e --arg version "$version" --arg commit "$commit" --arg operation "$operation" \
  --slurpfile previous "$work/current-release.json" --slurpfile patch "$work/patch.json" '
  .tag_name == $version and .target_commitish == $commit and
  .body == $patch[0].body and
  .draft == (if $operation == "repair" then $previous[0].draft else false end)
' "$work/result.json" >/dev/null || fail "GitHub did not retain the requested release state"
if [[ $publish_latest == true ]]; then
  [[ $(gh api "repos/$repository/releases/latest" --jq .tag_name) == "$version" ]] ||
    fail "GitHub latest release does not match the finalized candidate"
fi
echo "PASS $operation https://github.com/$repository/releases/tag/$version"
