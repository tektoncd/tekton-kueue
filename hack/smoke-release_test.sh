#!/usr/bin/env bash
set -euo pipefail

# The same executable supplies both boundary fakes. Local YAML parsing remains
# real kubectl, using the smoke script's isolated kubeconfig and --local.
if [[ ${SMOKE_FAKE:-} == true ]]; then
  command=$(basename "$0")
  [[ -n ${KUBECONFIG:-} && $KUBECONFIG != "$USER_KUBECONFIG" ]] || exit 90
  jq -cn --arg command "$command" --arg kubeconfig "$KUBECONFIG" \
    --args '$ARGS.positional as $args | {command:$command,kubeconfig:$kubeconfig,args:$args}' \
    -- "$@" >> "$CALLS"
  if [[ $command == kind ]]; then
    case "$*" in
      "create cluster "*)
        [[ $* == *"--kubeconfig $KUBECONFIG"* && $* == *"--wait 180s"* ]] || exit 91
        [[ $MODE != creation ]] || exit 42
        ;;
      "delete cluster "*)
        [[ $MODE != cleanup ]] || exit 42
        ;;
      "export logs "*) ;;
      *) exit 92 ;;
    esac
    exit 0
  fi
  if [[ $1 == label ]]; then
    [[ $* == *"--local"* ]] || exit 93
    exec "$REAL_KUBECTL" "$@"
  fi
  [[ $1 == --context=kind-release-smoke-* && $2 == --request-timeout=30s ]] || exit 94
  shift 2
  case "$1" in
    apply)
      if [[ $* == *"-f -"* ]]; then
        # Parse actual fixtures offline; invalid YAML cannot silently pass.
        "$REAL_KUBECTL" label --local --overwrite -f - -o json fixture-validation=true \
          > "$CASE_DIR/fixtures.json"
      elif [[ $* == *"/release.yaml"* && $* != *https:* ]]; then
        for ((i=1; i<=$#; i++)); do
          if [[ ${!i} == -f ]]; then
            j=$((i + 1))
            cmp "$VERIFIED_MANIFEST" "${!j}"
          fi
        done
      fi
      ;;
    rollout)
      if [[ $MODE == readiness && $* == *"status deployment/tekton-kueue-webhook"* ]]; then exit 42; fi
      ;;
    wait)
      [[ $* == *--timeout=* ]] || exit 95
      if [[ $MODE == run && $* == *condition=Succeeded* ]]; then exit 42; fi
      if [[ $MODE == admission && $* == *condition=Admitted* ]]; then exit 42; fi
      if [[ $MODE == workload && $* == *--for=create* ]]; then exit 42; fi
      ;;
    create)
      "$REAL_KUBECTL" label --local --overwrite -f - -o json fixture-validation=true \
        > "$CASE_DIR/pipelinerun.json"
      jq -n --arg mode "$MODE" '{
        metadata:{uid:"smoke-uid",
          labels:{"kueue.x-k8s.io/queue-name":"pipelines-queue"},
          annotations:{"kueue.konflux-ci.dev/requests-cpu":(if $mode == "mutation" then "2" else "1" end)}},
        spec:{status:"Pending"}}'
      ;;
    get)
      if [[ $2 == workload/* ]]; then
        jq -n '{metadata:{ownerReferences:[{uid:"smoke-uid",kind:"PipelineRun",controller:true}]},
          spec:{queueName:"pipelines-queue",podSets:[{template:{spec:{containers:[
            {resources:{requests:{cpu:"1","tekton.dev/pipelineruns":"1"}}}
          ]}}}]}}'
      elif [[ $* != *"-o name"* ]]; then
        printf 'kind: List\nitems: []\n'
      fi
      ;;
    describe|logs) ;;
    *) exit 96 ;;
  esac
  exit 0
fi

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
tmp=$(mktemp -d)
trap 'status=$?; if [[ $status == 0 ]]; then rm -rf "$tmp"; else echo "test artifacts: $tmp" >&2; fi' EXIT
export REAL_KUBECTL
REAL_KUBECTL=$(command -v kubectl)
mkdir "$tmp/bin"
cp "${BASH_SOURCE[0]}" "$tmp/bin/kubectl"
cp "${BASH_SOURCE[0]}" "$tmp/bin/kind"
chmod +x "$tmp/bin/kubectl" "$tmp/bin/kind"
export PATH="$tmp/bin:$PATH" SMOKE_FAKE=true
export USER_KUBECONFIG="$tmp/user-kubeconfig"
printf 'untouched user configuration\n' > "$USER_KUBECONFIG"
cp "$USER_KUBECONFIG" "$tmp/user-original"
image=ghcr.io/tektoncd/tekton-kueue@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
export CASE_DIR CALLS MODE VERIFIED_MANIFEST
cases=0

check() {
  jq -e -s "$1" "$CALLS" >/dev/null || { echo "failed assertion in $MODE: $1" >&2; exit 1; }
}
run_case() {
  MODE=$1
  CASE_DIR="$tmp/$MODE"
  mkdir "$CASE_DIR"
  CALLS="$CASE_DIR/calls.jsonl"
  : > "$CALLS"
  VERIFIED_MANIFEST="$CASE_DIR/release.yaml"
  local expected=$image
  cp "$root/test/release-smoke/release.yaml" "$VERIFIED_MANIFEST"
  case "$MODE" in
    digest) expected=ghcr.io/tektoncd/tekton-kueue:latest ;;
    mismatch) expected=${image/aaaa/bbbb} ;;
    malformed) printf 'not: [valid yaml\n' > "$VERIFIED_MANIFEST" ;;
    missing) rm "$VERIFIED_MANIFEST" ;;
    incomplete)
      # A valid document without the required controller/webhook pair.
      printf 'apiVersion: v1\nkind: Namespace\nmetadata:\n  name: test\n' > "$VERIFIED_MANIFEST"
      ;;
    artifacts) mkdir "$CASE_DIR/artifacts"; touch "$CASE_DIR/artifacts/keep" ;;
  esac
  local status=0
  KUBECONFIG="$USER_KUBECONFIG" bash "$root/hack/smoke-release.sh" \
    "$VERIFIED_MANIFEST" "$expected" "$CASE_DIR/artifacts" > "$CASE_DIR/output" 2>&1 || status=$?
  case "$MODE" in
    success|success-again) [[ $status == 0 ]] || { echo "success flow failed; see $CASE_DIR/output" >&2; "$REAL_KUBECTL" version --client; exit 1; } ;;
    *) [[ $status != 0 ]] || { echo "$MODE unexpectedly passed" >&2; exit 1; } ;;
  esac
  cmp "$USER_KUBECONFIG" "$tmp/user-original"
  case "$MODE" in
    digest|mismatch|malformed|missing|incomplete|artifacts)
      check 'all(.[]; .command != "kind")'
      ;;
    *)
      check '
        [.[] | select(.command == "kind" and .args[0] == "create")][0] as $create
        | ($create.args | index("--name")) as $nameIndex
        | $create.args[$nameIndex + 1] as $name
        | ($name | startswith("release-smoke-"))
          and all(.[] | select(.command == "kind");
            (.args | index("--name")) as $i | .args[$i + 1] == $name)
          and any(.[]; .command == "kind" and .args[0] == "delete")
          and all(.[] | select(.command == "kubectl" and .args[0] != "label");
            .args[0] == ("--context=kind-" + $name) and .kubeconfig == $create.kubeconfig)
      '
      [[ -s $CASE_DIR/artifacts/events.yaml && -f $CASE_DIR/artifacts/cleanup.log ]] ||
        { echo "missing failure diagnostics" >&2; exit 1; }
      # The private kubeconfig directory must disappear even after failures.
      local kubeconfig
      kubeconfig=$(jq -r 'select(.command == "kind" and .args[0] == "create") | .kubeconfig' "$CALLS")
      [[ ! -e $(dirname "$kubeconfig") ]]
      if [[ $MODE == success || $MODE == success-again ]]; then
        check '
          any(.[]; .args | index("--for=condition=Ready"))
          and any(.[]; .args | index("deployment/tekton-kueue-controller-manager"))
          and any(.[]; .args | index("deployment/tekton-kueue-webhook"))
          and any(.[]; .args | index("--for=create"))
          and any(.[]; .args | index("--for=condition=Admitted"))
          and any(.[]; .args | index("--for=condition=Succeeded"))
          and any(.[]; .args | index("https://github.com/cert-manager/cert-manager/releases/download/v1.19.2/cert-manager.yaml"))
          and any(.[]; .args | index("https://github.com/kubernetes-sigs/kueue/releases/download/v0.16.6/manifests.yaml"))
          and any(.[]; .args | index("https://github.com/tektoncd/pipeline/releases/download/v1.15.0/release.yaml"))
        '
        cmp "$VERIFIED_MANIFEST" "$CASE_DIR/artifacts/release.yaml"
        jq -e '.apiVersion == "tekton.dev/v1" and
          (.spec.pipelineSpec.tasks[0].taskSpec.steps[0].image | contains("@sha256:"))' \
          "$CASE_DIR/pipelinerun.json" >/dev/null
      fi
      ;;
  esac
  cases=$((cases + 1))
  echo "PASS $MODE"
}

for mode in digest mismatch malformed missing incomplete artifacts success success-again \
  creation readiness mutation workload admission run cleanup; do
  run_case "$mode"
done
first=$(jq -r 'select(.command == "kind" and .args[0] == "create") | .args[] | select(startswith("release-smoke-"))' "$tmp/success/calls.jsonl")
second=$(jq -r 'select(.command == "kind" and .args[0] == "create") | .args[] | select(startswith("release-smoke-"))' "$tmp/success-again/calls.jsonl")
[[ $first != "$second" ]]
echo "PASS $cases deterministic release smoke cases"
