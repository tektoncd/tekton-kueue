#!/usr/bin/env bash
set -euo pipefail

fail() { echo "release smoke failed: $*" >&2; exit 1; }
[[ $# == 3 ]] || fail "usage: $0 VERIFIED_MANIFEST EXPECTED_IMAGE ARTIFACT_DIR"
manifest=$1
image=$2
artifacts=$3
repository=ghcr.io/tektoncd/tekton-kueue
[[ $image =~ ^ghcr\.io/tektoncd/tekton-kueue@sha256:[0-9a-f]{64}$ ]] ||
  fail "EXPECTED_IMAGE must be the canonical digest reference"
[[ -f $manifest && -r $manifest && -s $manifest ]] || fail "manifest must be a nonempty readable file"
[[ -n $artifacts && ! -L $artifacts ]] || fail "invalid artifact directory"
if [[ -e $artifacts ]]; then
  [[ -d $artifacts && -z $(find "$artifacts" -mindepth 1 -print -quit) ]] ||
    fail "artifact directory must be empty"
fi
for command in kind kubectl jq openssl; do
  command -v "$command" >/dev/null || fail "missing command: $command"
done
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
work=$(mktemp -d)
cluster="release-smoke-$(basename "$work" | tr '[:upper:]._' '[:lower:]--')"
context="kind-$cluster"
export KUBECONFIG="$work/kubeconfig"
owned=false

k() { kubectl --context="$context" --request-timeout=30s "$@"; }
cleanup() {
  status=$?
  trap - EXIT
  set +e
  if [[ $owned == true ]]; then
    for resource in pods deployments services endpoints events certificates \
      pipelineruns.tekton.dev taskruns.tekton.dev workloads.kueue.x-k8s.io; do
      k get "$resource" -A -o yaml > "$artifacts/$resource.yaml" 2>&1
    done
    k describe pods -A > "$artifacts/pod-descriptions.txt" 2>&1
    for namespace in cert-manager kueue-system tekton-pipelines tekton-kueue release-smoke; do
      pods=$(k get pods -n "$namespace" -o name)
      for pod in $pods; do
        k logs -n "$namespace" "$pod" --all-containers=true --prefix=true \
          --tail=200 > "$artifacts/$namespace-${pod#*/}.log" 2>&1
      done
    done
    kind export logs --name "$cluster" "$artifacts/kind" > "$artifacts/kind-export.log" 2>&1
    kind delete cluster --name "$cluster" > "$artifacts/cleanup.log" 2>&1
    if [[ $? != 0 ]]; then
      echo "failed to delete owned cluster: $cluster" >&2
      status=1
    fi
  fi
  rm -rf "$work"
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# Parse the finished YAML locally, without discovery or the user's kubeconfig.
# This labels only the in-memory objects, never the installed release.
cp "$manifest" "$work/release.yaml"
kubectl label --local --overwrite -f "$work/release.yaml" -o json release-smoke-validation=true > "$work/manifest.json"
jq -e -s --arg repo "$repository" --arg digest "${image##*@}" '
  [ .[] | if .kind == "List" then .items[] else . end ] as $objects
  | [$objects[] | select(.kind == "Deployment")] as $deployments
  | ($deployments | length == 2)
    and ($deployments | map(.metadata.name) | sort ==
      ["tekton-kueue-controller-manager", "tekton-kueue-webhook"])
    and all($deployments[];
      .apiVersion == "apps/v1" and .metadata.namespace == "tekton-kueue"
      and (.spec.template.spec.containers | length == 1)
      and ((.spec.template.spec.initContainers // []) | length == 0))
    and ([$objects[] | .. | objects | select(has("image")) | .image] |
      length == 2 and all(.[];
        type == "string" and
        (split("@") | length == 2 and .[1] == $digest
          and (.[0] == $repo or
            (.[0] | startswith($repo + ":") and
              (ltrimstr($repo + ":") | test("^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$")))))))
' "$work/manifest.json" >/dev/null || fail "manifest controller/webhook images do not match EXPECTED_IMAGE"

mkdir -p "$artifacts"
artifacts=$(cd "$artifacts" && pwd)
cp "$work/release.yaml" "$artifacts/release.yaml"
printf '%s\n' "$image" > "$artifacts/expected-image.txt"
printf '%s\n' "$cluster" > "$artifacts/cluster-name.txt"
# Install only the snapshot checked above, even if the caller changes its file.
exec > >(tee "$artifacts/smoke.log") 2>&1
owned=true # Creation can fail after provisioning nodes; still clean up our name.
kind create cluster --name "$cluster" --kubeconfig "$KUBECONFIG" --wait 180s --retain \
  --image kindest/node:v1.35.5@sha256:ce977ae6d65918d0b58a5f8b5e940429c2ce42fa3a5619ec2bbc60b949c0ac95

k apply --server-side -f https://github.com/cert-manager/cert-manager/releases/download/v1.19.2/cert-manager.yaml
k wait --for=condition=Available deployment --all -n cert-manager --timeout=300s
k apply --server-side -f https://github.com/kubernetes-sigs/kueue/releases/download/v0.16.6/manifests.yaml \
  -f "$root/hack/kueue-config.yaml"
k rollout restart deployment/kueue-controller-manager -n kueue-system
k rollout status deployment/kueue-controller-manager -n kueue-system --timeout=300s
k apply --server-side -f https://github.com/tektoncd/pipeline/releases/download/v1.15.0/release.yaml
k wait --for=condition=Available deployment --all -n tekton-pipelines --timeout=300s
k apply --server-side -f "$work/release.yaml"
k wait --for=condition=Ready certificate --all -n tekton-kueue --timeout=300s
k rollout status deployment/tekton-kueue-controller-manager -n tekton-kueue --timeout=300s
k rollout status deployment/tekton-kueue-webhook -n tekton-kueue --timeout=300s
k wait --for=jsonpath='{.webhooks[0].clientConfig.caBundle}' \
  mutatingwebhookconfiguration/tekton-kueue-mutating-webhook-configuration --timeout=300s

# Configure one CPU resource mutation. Restart after configuration so the new
# webhook starts with the ConfigMap already present, rather than racing reload.
k apply -f - <<'YAML'
apiVersion: v1
kind: ConfigMap
metadata:
  name: tekton-kueue-config
  namespace: tekton-kueue
data:
  config.yaml: |
    queueName: pipelines-queue
    cel:
      expressions:
        - resource("cpu", 1)
---
apiVersion: v1
kind: Namespace
metadata:
  name: release-smoke
---
apiVersion: kueue.x-k8s.io/v1beta2
kind: ResourceFlavor
metadata:
  name: release-smoke
spec: {}
---
apiVersion: kueue.x-k8s.io/v1beta2
kind: ClusterQueue
metadata:
  name: release-smoke
spec:
  namespaceSelector: {}
  resourceGroups:
    - coveredResources: ["cpu", "tekton.dev/pipelineruns"]
      flavors:
        - name: release-smoke
          resources:
            - name: cpu
              nominalQuota: 1
            - name: tekton.dev/pipelineruns
              nominalQuota: 1
---
apiVersion: kueue.x-k8s.io/v1beta2
kind: LocalQueue
metadata:
  name: pipelines-queue
  namespace: release-smoke
spec:
  clusterQueue: release-smoke
YAML
k rollout restart deployment/tekton-kueue-webhook -n tekton-kueue
k rollout status deployment/tekton-kueue-webhook -n tekton-kueue --timeout=300s
k wait --for=condition=Active clusterqueue/release-smoke --timeout=120s
k wait --for=condition=Active localqueue/pipelines-queue -n release-smoke --timeout=120s
k create -f - -o json > "$artifacts/created-pipelinerun.json" <<'YAML'
apiVersion: tekton.dev/v1
kind: PipelineRun
metadata:
  name: release-smoke
  namespace: release-smoke
spec:
  timeouts:
    pipeline: 5m0s
  pipelineSpec:
    tasks:
      - name: hello
        taskSpec:
          steps:
            - name: hello
              image: registry.access.redhat.com/ubi9/ubi@sha256:66233eebd72bb5baa25190d4f55e1dc3fff3a9b77186c1f91a0abdb274452072
              command: ["/bin/echo", "release smoke"]
YAML
# The create response is the admission result, before controller updates.
jq -e '.spec.status == "PipelineRunPending"
  and .metadata.labels["kueue.x-k8s.io/queue-name"] == "pipelines-queue"
  and .metadata.annotations["kueue.konflux-ci.dev/requests-cpu"] == "1"
  and (.metadata.uid | type == "string" and length > 0)
' "$artifacts/created-pipelinerun.json" >/dev/null || fail "webhook mutation missing"
uid=$(jq -r .metadata.uid "$artifacts/created-pipelinerun.json")
# Kueue v0.16.6 GetWorkloadNameForOwnerWithGVK hashes kind, group, name, UID.
hash=$(printf 'PipelineRun\ntekton.dev\nrelease-smoke\n%s' "$uid" | openssl dgst -sha1 -r)
workload="workload/pipelinerun-release-smoke-${hash:0:5}"
k wait --for=create "$workload" -n release-smoke --timeout=120s
k get "$workload" -n release-smoke -o json > "$artifacts/created-workload.json"
jq -e --arg uid "$uid" '
  any(.metadata.ownerReferences[]; .uid == $uid and .kind == "PipelineRun" and .controller == true)
  and .spec.queueName == "pipelines-queue"
  and .spec.podSets[0].template.spec.containers[0].resources.requests.cpu == "1"
  and .spec.podSets[0].template.spec.containers[0].resources.requests["tekton.dev/pipelineruns"] == "1"
' "$artifacts/created-workload.json" >/dev/null || fail "Workload does not represent the mutated PipelineRun"
k wait --for=condition=Admitted "$workload" -n release-smoke --timeout=300s
k wait --for=condition=Succeeded pipelinerun/release-smoke -n release-smoke --timeout=360s
echo "PASS published release $image"
