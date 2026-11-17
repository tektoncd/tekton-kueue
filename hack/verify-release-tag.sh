#!/usr/bin/env bash
set -euo pipefail

fail() { echo "release tag verification failed: $*" >&2; exit 1; }
[[ $# == 3 ]] || fail "usage: $0 VERSION COMMIT ALLOW_MISSING"
version=$1
commit=$2
allow_missing=$3
repository=${GITHUB_REPOSITORY:-tektoncd/tekton-kueue}
[[ $version =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ && $commit =~ ^[0-9a-f]{40}$ &&
   $repository =~ ^[A-Za-z0-9][A-Za-z0-9-]*/[A-Za-z0-9_.-]+$ &&
   ( $allow_missing == true || $allow_missing == false ) ]] || fail "invalid candidate"
object=$(gh api --paginate --slurp "repos/$repository/git/matching-refs/tags/$version" |
  jq -c --arg ref "refs/tags/$version" '
    [.[][] | select(.ref == $ref)]
    | if length <= 1 then .[0].object // null else error("ambiguous tag") end')
if [[ $object == null ]]; then
  [[ $allow_missing == true ]] || fail "published release tag is missing"
  exit 0
fi
while :; do
  sha=$(jq -r .sha <<< "$object")
  [[ $sha =~ ^[0-9a-f]{40}$ ]] || fail "invalid tag object"
  case $(jq -r .type <<< "$object") in
    commit)
      [[ $sha == "$commit" ]] || fail "tag points to another source"
      exit 0
      ;;
    tag)
      object=$(gh api "repos/$repository/git/tags/$sha" | jq -ce '.object')
      ;;
    *) fail "tag does not resolve to a commit" ;;
  esac
done
