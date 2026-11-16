#!/usr/bin/env bash

set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)

run_scenario() {
  local scenario=$1
  local summary
  summary=$(mktemp)
  trap 'rm -f "$summary"' RETURN

  gh() {
    case "$2" in
      */git/ref/heads/*)
        printf '%s\n' abc123
        ;;
      */check-runs*)
        case "$SCENARIO" in
          failure)
            printf '%s\n' \
              '{"status":"completed","conclusion":"failure","details_url":"https://example.test/run"}'
            ;;
          timeout)
            ;;
          *)
            printf '%s\n' \
              '{"status":"completed","conclusion":"success","details_url":"https://example.test/run"}'
            ;;
        esac
        ;;
      */releases*)
        [ "$SCENARIO" = "missing-draft" ] ||
          printf '%s\n' '{"html_url":"https://example.test/draft"}'
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
    :
  }
  export -f curl date gh sleep

  local status=0
  local draft_timeout=1
  local wait_timeout=1
  [ "$scenario" = "missing-draft" ] && draft_timeout=0
  [ "$scenario" = "timeout" ] && wait_timeout=0
  SCENARIO=$scenario \
    GH_TOKEN=test \
    GITHUB_REPOSITORY=tektoncd/tekton-kueue \
    GITHUB_STEP_SUMMARY=$summary \
    PAC_CONTROLLER_URL=https://example.test \
    PAC_DRAFT_WAIT_TIMEOUT_SECONDS=$draft_timeout \
    PAC_INCOMING_SECRET=secret \
    PAC_REPOSITORY_NAME=tektoncd-tekton-kueue \
    PAC_WAIT_TIMEOUT_SECONDS=$wait_timeout \
    RELEASE_AS_LATEST=true \
    RELEASE_BRANCH=release-v0.4.x \
    RELEASE_VERSION=v0.4.3 \
    bash "$root/hack/trigger-patch-release.sh" || status=$?

  case "$scenario" in
    success)
      test "$status" -eq 0
      grep -Fq "https://example.test/run" "$summary"
      grep -Fq "https://example.test/draft" "$summary"
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
