#!/usr/bin/env bash

set -euo pipefail

image=${1:?usage: render-manifest.sh IMAGE}
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
kustomize=${KUSTOMIZE:-kustomize}

if [[ "$kustomize" != */* ]]; then
  kustomize=$(command -v "$kustomize")
elif [[ "$kustomize" != /* ]]; then
  kustomize="$root/$kustomize"
fi

overlay=$(mktemp -d "$root/.release-overlay.XXXXXX")
trap 'rm -rf "$overlay"' EXIT

cat > "$overlay/kustomization.yaml" <<'EOF'
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
- ../config/default
EOF

(
  cd "$overlay"
  "$kustomize" edit set image "ghcr.io/tektoncd/tekton-kueue=$image"
)

"$kustomize" build "$overlay"
