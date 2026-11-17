#!/usr/bin/env bash
set -euo pipefail

fail() { echo "release recovery failed: $*" >&2; exit 1; }
robot() {
  [[ $(gh api user --jq .login) == tekton-robot ]] || fail "GH_TOKEN must belong to tekton-robot"
}
[[ $# == 5 ]] || fail "usage: $0 VERSION COMMIT DIGEST PUBLIC_KEY_FILE WORK_DIR"
version=$1
commit=$2
digest=$3
key=$4
work=$5
repository=tektoncd/tekton-kueue
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
[[ $version =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ && $commit =~ ^[0-9a-f]{40}$ &&
   $digest =~ ^sha256:[0-9a-f]{64}$ ]] || fail "invalid candidate identity"
[[ ${GITHUB_REPOSITORY:-} == "$repository" && ${GITHUB_REF:-} == refs/heads/main ]] ||
  fail "run the trusted main-branch workflow"
[[ -n ${GH_TOKEN:-} && -s $key && -r $key ]] || fail "robot token and trusted key are required"
[[ -n $work && ! -L $work ]] || fail "invalid work directory"
if [[ -e $work ]]; then
  [[ -d $work && -z $(find "$work" -mindepth 1 -print -quit) ]] || fail "work directory must be empty"
fi
mkdir -p "$work"
work=$(cd "$work" && pwd)
robot
[[ $(gh api "repos/$repository" --jq .permissions.push) == true ]] ||
  fail "repository push permission is required to inspect drafts"
gh api "repos/$repository/environments/kueue-release-finalization" > "$work/environment.json"
jq -e '
  .can_admins_bypass == false and
  any(.protection_rules[]?;
    .type == "required_reviewers" and .prevent_self_review == true and
    any(.reviewers[]?; .type == "Team" and .reviewer.id == 18766447))
' "$work/environment.json" >/dev/null || fail "release approval environment is not protected"

# No GitHub mutation is allowed until a complete, signed existing bundle passes.
bash "$root/hack/verify-release-artifacts.sh" "$version" "$commit" bucket "$key" "$work/verified"
jq -e --arg digest "$digest" '.digest == $digest' "$work/verified/release-info.json" >/dev/null ||
  fail "verified digest differs from approved candidate"

check_tag() {
  local object sha
  gh api --paginate --slurp "repos/$repository/git/matching-refs/tags/$version" > "$work/refs.json"
  jq -e --arg ref "refs/tags/$version" '
    [.[][] | select(.ref == $ref)] | length <= 1
  ' "$work/refs.json" >/dev/null || fail "ambiguous tag"
  object=$(jq -c --arg ref "refs/tags/$version" '
    [.[][] | select(.ref == $ref)][0].object // null' "$work/refs.json")
  while [[ $object != null ]]; do
    sha=$(jq -r .sha <<< "$object")
    [[ $sha =~ ^[0-9a-f]{40}$ ]] || fail "invalid tag object"
    case $(jq -r .type <<< "$object") in
      commit) [[ $sha == "$commit" ]] || fail "tag points to another source"; return ;;
      tag)
        gh api "repos/$repository/git/tags/$sha" > "$work/tag.json"
        object=$(jq -ce '.object' "$work/tag.json")
        ;;
      *) fail "tag does not resolve to a commit" ;;
    esac
  done
}

find_release() {
  gh api --paginate --slurp "repos/$repository/releases?per_page=100" > "$work/releases.json"
  jq --arg version "$version" '[.[][] | select(.tag_name == $version)]' \
    "$work/releases.json" > "$work/candidates.json"
  jq -e 'length <= 1' "$work/candidates.json" >/dev/null || fail "multiple matching releases"
  release_id=$(jq -r '.[0].id // empty' "$work/candidates.json")
  [[ -z $release_id || $release_id =~ ^[1-9][0-9]*$ ]] || fail "invalid release ID"
}

refresh() {
  local file asset_id
  check_tag
  gh api "repos/$repository/releases/$release_id" > "$work/current.json"
  jq -e --arg id "$release_id" --arg version "$version" --arg commit "$commit" '
    .id == ($id | tonumber) and .tag_name == $version
    and .target_commitish == $commit and .draft == true
    and (.assets | type == "array")
    and ([.assets[] | select(.name == "release.yaml" or .name == "release.notags.yaml" or .name == "SHA256SUMS")]
      | (map(.name) | unique | length) == length
      and all(.[]; (.id | type == "number") and .id > 0 and .id == (.id | floor))
      and (map(.id) | unique | length) == length)
  ' "$work/current.json" >/dev/null || fail "release source, publication state or assets conflict"
  for file in release.yaml release.notags.yaml SHA256SUMS; do
    asset_id=$(jq -r --arg name "$file" '.assets[] | select(.name == $name) | .id' "$work/current.json")
    if [[ -n $asset_id ]]; then
      gh api --method GET -H 'Accept: application/octet-stream' \
        "repos/$repository/releases/assets/$asset_id" > "$work/existing-$file"
      cmp -s "$work/existing-$file" "$work/verified/$file" || fail "conflicting existing asset: $file"
    fi
  done
}

check_tag
find_release
if [[ -z $release_id ]]; then
  gh api --method POST "repos/$repository/releases/generate-notes" \
    -f tag_name="$version" -f target_commitish="$commit" > "$work/notes.json"
  jq -e '.body | type == "string" and length > 0' "$work/notes.json" >/dev/null ||
    fail "cannot generate release notes"
  # Recheck absence immediately before creating; never replace another draft.
  check_tag
  find_release
  if [[ -z $release_id ]]; then
    jq --arg version "$version" --arg commit "$commit" '
      {tag_name:$version,target_commitish:$commit,draft:true,
       name:$version,body:.body,make_latest:"false"}' "$work/notes.json" > "$work/create.json"
    robot
    gh api --method POST "repos/$repository/releases" --input "$work/create.json" > "$work/created.json"
    release_id=$(jq -er '.id | select(type == "number" and . > 0 and . == floor)' "$work/created.json")
  fi
fi
for file in release.yaml release.notags.yaml SHA256SUMS; do
  refresh
  if ! jq -e --arg name "$file" 'any(.assets[]; .name == $name)' "$work/current.json" >/dev/null; then
    robot
    # No --clobber: a concurrent attachment must fail, never replace bytes.
    gh release upload "$version" "$work/verified/$file" --repo "$repository"
  fi
done
refresh
bash "$root/hack/verify-release-artifacts.sh" "$version" "$commit" "$release_id" "$key" "$work/recovered"
jq -e --arg digest "$digest" '.digest == $digest and .draft == true' \
  "$work/recovered/release-info.json" >/dev/null || fail "recovered draft verification failed"
printf 'Verified draft ID: %s\nFinalize separately after approval; no rebuild or latest promotion occurred.\n' "$release_id"
