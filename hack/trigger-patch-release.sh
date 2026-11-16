#!/usr/bin/env bash

set -euo pipefail

: "${GITHUB_REPOSITORY:?}"
: "${GITHUB_STEP_SUMMARY:?}"
: "${PAC_CONTROLLER_URL:?}"
: "${PAC_INCOMING_SECRET:?}"
: "${PAC_REPOSITORY_NAME:?}"
: "${RELEASE_AS_LATEST:?}"
: "${RELEASE_BRANCH:?}"
: "${RELEASE_VERSION:?}"

check_name="Tekton CI (Pipelines-as-Code) / release-patch"
triggered_at=$(date -u +'%Y-%m-%dT%H:%M:%SZ')
branch_sha=$(gh api \
  "repos/${GITHUB_REPOSITORY}/git/ref/heads/${RELEASE_BRANCH}" \
  --jq '.object.sha')

jq -n '{
  repository: env.PAC_REPOSITORY_NAME,
  branch: env.RELEASE_BRANCH,
  pipelinerun: "release-patch",
  secret: env.PAC_INCOMING_SECRET,
  params: {
    version: env.RELEASE_VERSION,
    release_as_latest: env.RELEASE_AS_LATEST
  }
}' | curl --fail-with-body --silent --show-error \
  -H "Content-Type: application/json" \
  --data-binary @- \
  "${PAC_CONTROLLER_URL}/incoming"

deadline=$((SECONDS + 10800))
check=""
check_id=""
while [ "$SECONDS" -lt "$deadline" ]; do
  if [ -n "$check_id" ]; then
    check=$(gh api "repos/${GITHUB_REPOSITORY}/check-runs/${check_id}")
  else
    # PaC resolves the branch after dispatch, possibly after another push.
    new_commits=$(gh api --paginate \
      "repos/${GITHUB_REPOSITORY}/compare/${branch_sha}...${RELEASE_BRANCH}?per_page=100" \
      --jq '.commits[].sha')
    for commit_sha in "$branch_sha" $new_commits; do
      check=$(gh api \
        "repos/${GITHUB_REPOSITORY}/commits/${commit_sha}/check-runs?per_page=100" \
        --jq "[.check_runs[]
          | select(.name == \"${check_name}\")
          | select(.started_at != null)
          | select(.started_at >= \"${triggered_at}\")]
          | sort_by(.started_at)
          | .[0] // empty")
      if [ -n "$check" ]; then
        # Follow this run even if the branch advances again.
        check_id=$(jq -r '.id' <<<"$check")
        break
      fi
    done
  fi

  if [ -n "$check" ] && [ "$(jq -r '.status' <<<"$check")" = "completed" ]; then
    break
  fi
  sleep 20
done

if [ -z "$check" ] || [ "$(jq -r '.status' <<<"$check")" != "completed" ]; then
  echo "::error::Timed out waiting for the external ${RELEASE_VERSION} release"
  exit 1
fi

details_url=$(jq -r '.details_url' <<<"$check")
conclusion=$(jq -r '.conclusion' <<<"$check")
if [ "$conclusion" != "success" ]; then
  echo "::error::External ${RELEASE_VERSION} release concluded ${conclusion}: ${details_url}"
  exit 1
fi

draft=""
draft_deadline=$((SECONDS + 60))
while [ "$SECONDS" -lt "$draft_deadline" ]; do
  draft=$(gh api "repos/${GITHUB_REPOSITORY}/releases?per_page=100" \
    --jq "[.[] | select(.tag_name == \"${RELEASE_VERSION}\" and .draft)] | .[0] // empty")
  [ -n "$draft" ] && break
  sleep 5
done

if [ -z "$draft" ]; then
  echo "::error::External release succeeded but no ${RELEASE_VERSION} draft release was created"
  exit 1
fi

draft_url=$(jq -r '.html_url' <<<"$draft")
{
  echo "### ${RELEASE_VERSION} patch release"
  echo
  echo "- External PipelineRun: ${details_url}"
  echo "- Draft release: ${draft_url}"
  echo "- Publication: manual review required"
} >> "$GITHUB_STEP_SUMMARY"

echo "::warning title=Draft release awaiting publication::Review and publish ${draft_url}"
