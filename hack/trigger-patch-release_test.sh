#!/usr/bin/env bash

set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)

run_scenario() {
  local scenario=$1
  local tmp
  tmp=$(mktemp -d)
  local summary=$tmp/summary
  trap 'rm -rf "$tmp"' RETURN
  : > "$summary"

  gh() {
    local endpoint=$2
    shift 2
    local filter=""
    while [ "$#" -gt 0 ]; do
      case "$1" in
        --jq)
          filter=$2
          shift 2
          ;;
        *)
          shift
          ;;
      esac
    done

    case "$endpoint" in
      */git/ref/heads/*)
        printf '%s\n' '{"object":{"sha":"abc123"}}' |
          jq -c -r "$filter"
        ;;
      */check-runs*)
        local calls
        calls=$(cat "$GH_STATE_DIR/check-count" 2>/dev/null || printf 0)
        calls=$((calls + 1))
        printf '%s\n' "$calls" > "$GH_STATE_DIR/check-count"

        local conclusion=success
        local status=in_progress
        [ "$calls" -ge 2 ] && status=completed
        [ "$SCENARIO" = "failure" ] && conclusion=failure
        case "$SCENARIO" in
          timeout)
            status=completed
            ;;
          *)
            ;;
        esac

        jq -nc \
          --arg conclusion "$conclusion" \
          --arg status "$status" \
          '{
            check_runs: [
              {
                name: "Tekton CI (Pipelines-as-Code) / release-patch",
                started_at: "2026-11-16T21:29:59Z",
                status: "completed",
                conclusion: "failure",
                details_url: "https://example.test/run-old"
              },
              {
                name: "unrelated",
                started_at: "2026-11-16T21:30:01Z",
                status: "completed",
                conclusion: "success",
                details_url: "https://example.test/run-unrelated"
              },
              {
                name: (
                  if env.SCENARIO == "timeout"
                  then "unrelated"
                  else "Tekton CI (Pipelines-as-Code) / release-patch"
                  end
                ),
                started_at: "2026-11-16T21:30:01Z",
                status: $status,
                conclusion: $conclusion,
                details_url: "https://example.test/run-current"
              }
            ]
          }' |
          jq -c -r "$filter"
        ;;
      */releases*)
        local calls
        calls=$(cat "$GH_STATE_DIR/release-count" 2>/dev/null || printf 0)
        calls=$((calls + 1))
        printf '%s\n' "$calls" > "$GH_STATE_DIR/release-count"

        if [ "$SCENARIO" = "missing-draft" ]; then
          printf '%s\n' \
            '[{"tag_name":"v0.4.3","draft":false,"html_url":"https://example.test/published"}]'
        else
          printf '%s\n' \
            '[{"tag_name":"v0.4.2","draft":true,"html_url":"https://example.test/old-draft"},{"tag_name":"v0.4.3","draft":true,"html_url":"https://example.test/draft"}]'
        fi | jq -c -r "$filter"
        ;;
      *)
        return 1
        ;;
    esac
  }
  curl() {
    cat >/dev/null
  }
  date() {
    printf '%s\n' "2026-11-16T21:30:00Z"
  }
  sleep() {
    SECONDS=$((SECONDS + 1))
  }
  export -f curl date gh sleep

  local status=0
  local draft_timeout=2
  local wait_timeout=2
  SCENARIO=$scenario \
    GH_STATE_DIR=$tmp \
    GH_TOKEN=test \
    GITHUB_REPOSITORY=tektoncd/tekton-kueue \
    GITHUB_STEP_SUMMARY=$summary \
    PAC_CONTROLLER_URL=https://example.test \
    PAC_DRAFT_POLL_INTERVAL_SECONDS=1 \
    PAC_DRAFT_WAIT_TIMEOUT_SECONDS=$draft_timeout \
    PAC_INCOMING_SECRET=secret \
    PAC_POLL_INTERVAL_SECONDS=1 \
    PAC_REPOSITORY_NAME=tektoncd-tekton-kueue \
    PAC_WAIT_TIMEOUT_SECONDS=$wait_timeout \
    RELEASE_AS_LATEST=true \
    RELEASE_BRANCH=release-v0.4.x \
    RELEASE_VERSION=v0.4.3 \
    bash "$root/hack/trigger-patch-release.sh" > "$tmp/output" 2>&1 || status=$?

  test "$(cat "$tmp/check-count")" -ge 2

  case "$scenario" in
    success)
      test "$status" -eq 0
      grep -Fq "https://example.test/run-current" "$summary"
      grep -Fq "https://example.test/draft" "$summary"
      ;;
    failure)
      test "$status" -ne 0
      grep -Fq "https://example.test/run-current" "$tmp/output"
      ;;
    missing-draft)
      test "$status" -ne 0
      test "$(cat "$tmp/release-count")" -ge 2
      ;;
    *)
      test "$status" -ne 0
      ;;
  esac
}

run_scenario success
run_scenario failure
run_scenario timeout
run_scenario missing-draft
