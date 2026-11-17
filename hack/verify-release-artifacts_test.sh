#!/usr/bin/env bash

set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
export FIXTURES="$work/fixtures"
mkdir -p "$work/bin" "$FIXTURES"
export PATH="$work/bin:$PATH"
export GH_TOKEN=test-token
unset GITHUB_REPOSITORY
export VERSION=v0.4.3
export COMMIT=0123456789abcdef0123456789abcdef01234567
export DIGEST=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
export IMAGE="ghcr.io/tektoncd/tekton-kueue@sha256:$DIGEST"

# A fixed Ed25519 public key; openssl (not a fake) validates this boundary.
cat > "$work/key.pub" <<'EOF'
-----BEGIN PUBLIC KEY-----
MCowBQYDK2VwAyEA11qYAYKxCrfVS/7TyWQHOg7hcvPapiMlrwIaaPcHURo=
-----END PUBLIC KEY-----
EOF
write_artifacts() {
for variant in release.yaml release.notags.yaml; do
  reference=$IMAGE
  [[ $variant != release.yaml ]] || reference="ghcr.io/tektoncd/tekton-kueue:$VERSION@sha256:$DIGEST"
  for deployment in controller-manager webhook; do
    name=$deployment
    candidate=$reference
    case "${SCENARIO:-success}" in
      wrong-image-repository) candidate=${candidate/tektoncd\/tekton-kueue/other\/tekton-kueue} ;;
      inconsistent-image) [[ $deployment != webhook ]] || candidate=${candidate/$DIGEST/${DIGEST//a/b}} ;;
      missing-webhook) [[ $deployment != webhook ]] || continue ;;
      wrong-deployment) [[ $deployment != webhook ]] || name=other ;;
      tag-in-notags) [[ $variant != release.notags.yaml ]] || candidate="ghcr.io/tektoncd/tekton-kueue:$VERSION@sha256:$DIGEST" ;;
    esac
    cat <<EOF
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: tekton-kueue-$name
spec:
  template:
    spec:
      containers:
      - args:
        - ${deployment%-manager}
        image: $candidate
        name: manager
EOF
  done > "$FIXTURES/$variant"
done
(
  cd "$FIXTURES"
  if command -v sha256sum >/dev/null; then
    sha256sum release.yaml release.notags.yaml
  else
    shasum -a 256 release.yaml release.notags.yaml
  fi
) > "$FIXTURES/SHA256SUMS"
}

# Only network/registry/signing executables are replaced. JSON parsing, hashing,
# key parsing, manifest extraction and byte comparisons use the real commands.
cat > "$work/bin/fake" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
case "${0##*/}" in
  gh)
    [[ $1 == api && $2 == --method && $3 == GET ]] || exit 90
    if [[ $4 == repos/tektoncd/tekton-kueue/releases/123 ]]; then
      if [[ $SCENARIO == malformed-metadata ]]; then printf '{\n'; exit; fi
      jq -n --arg version "$VERSION" --arg commit "$COMMIT" --arg scenario "$SCENARIO" '
        {id:123,tag_name:$version,target_commitish:$commit,draft:true,assets:[
          {id:11,name:"release.yaml"},{id:12,name:"release.notags.yaml"},{id:13,name:"SHA256SUMS"}]}
        | if $scenario == "published" then .draft = false
          elif $scenario == "metadata-id" then .id = 124
          elif $scenario == "metadata-tag" then .tag_name = "v0.4.2"
          elif $scenario == "metadata-commit" then .target_commitish = "main"
          elif $scenario == "metadata-draft" then .draft = "true"
          elif $scenario == "metadata-assets" then .assets = null
          elif $scenario == "duplicate-asset" then .assets += [.assets[0]]
          elif $scenario == "missing-asset" then .assets |= .[0:2]
          elif $scenario == "unsafe-asset-id" then .assets[0].id = "../evil"
          elif $scenario == "duplicate-asset-id" then .assets[1].id = 11
          else . end'
    else
      [[ $4 == -H && $5 == "Accept: application/octet-stream" ]] || exit 90
      case "$6" in
        repos/tektoncd/tekton-kueue/releases/assets/11) file=release.yaml ;;
        repos/tektoncd/tekton-kueue/releases/assets/12) file=release.notags.yaml ;;
        repos/tektoncd/tekton-kueue/releases/assets/13) file=SHA256SUMS ;;
        *) exit 90 ;;
      esac
      if [[ $file == SHA256SUMS ]]; then
        case "$SCENARIO" in
          checksum) printf '%064d  release.yaml\n' 0; exit ;;
          traversal) printf '%s  ../release.yaml\n' "$DIGEST"; exit ;;
          absolute) printf '%s  /tmp/release.yaml\n' "$DIGEST"; exit ;;
          extra-checksum) cat "$FIXTURES/$file"; printf '%s  extra.yaml\n' "$DIGEST"; exit ;;
          duplicate-checksum) cat "$FIXTURES/$file" "$FIXTURES/$file"; exit ;;
          missing-checksum) awk 'NR == 1' "$FIXTURES/$file"; exit ;;
          empty-checksum) exit ;;
        esac
      fi
      cat "$FIXTURES/$file"
    fi
    ;;
  curl)
    [[ $# == 7 && $1 == --fail && $2 == --silent && $3 == --show-error &&
       $4 == --location && $6 == --output ]] || exit 90
    prefix="https://infra.tekton.dev/tekton-releases/tekton-kueue/previous/$VERSION/"
    [[ $5 == "$prefix"* ]] || exit 90
    file=${5#"$prefix"}
    case "$file" in release.yaml|release.notags.yaml|SHA256SUMS) ;; *) exit 90 ;; esac
    cat "$FIXTURES/$file" > "$7"
    [[ $SCENARIO != "bucket-$file" ]] || printf '\nchanged\n' >> "$7"
    ;;
  crane)
    if [[ $1 == manifest && $2 == "$IMAGE" && $# == 2 ]]; then
      jq -n --arg scenario "$SCENARIO" '
        ["amd64","arm64","ppc64le","s390x"]
        | if $scenario == "missing-platform" then .[0:3]
          elif $scenario == "duplicate-platform" then . + ["amd64"]
          elif $scenario == "wrong-platform" then .[3] = "riscv64"
          else . end
        | {schemaVersion:2, manifests:map({digest:("sha256:" + ("b" * 64)),
            platform:{os:"linux",architecture:.}})}'
    elif [[ $1 == config && $2 == --platform && $4 == "$IMAGE" && $# == 4 ]]; then
      jq -n --arg platform "$3" --arg scenario "$SCENARIO" '
        {os:"linux",architecture:($platform | split("/")[1]),config:{Labels:{
          "org.opencontainers.image.source":"https://github.com/tektoncd/tekton-kueue",
          "org.opencontainers.image.url":"https://github.com/tektoncd/tekton-kueue"}}}
        | if $platform == "linux/s390x" then
            if $scenario == "bad-source-label" then .config.Labels["org.opencontainers.image.source"] = "https://example.com"
            elif $scenario == "bad-url-label" then del(.config.Labels["org.opencontainers.image.url"])
            elif $scenario == "bad-config-platform" then .os = "windows"
            else . end
          else . end'
    else exit 90; fi
    ;;
  cosign)
    [[ $2 == --key && -s $3 ]] || exit 90
    case "$1" in
      verify)
        [[ $# == 4 && $4 == "$IMAGE" ]] || exit 90
        [[ $SCENARIO != signature-failure ]] || exit 1
        [[ $SCENARIO != empty-signature ]] || exit
        printf '[{"critical":{"image":{"docker-manifest-digest":"sha256:%s"}}}]\n' "$DIGEST"
        ;;
      verify-attestation)
        [[ $# == 6 && $4 == --type && $5 == https://slsa.dev/provenance/v1 && $6 == "$IMAGE" ]] || exit 90
        [[ $SCENARIO != attestation-failure ]] || exit 1
        [[ $SCENARIO != empty-attestation ]] || exit
        if [[ $SCENARIO == malformed-attestation ]]; then
          printf '{"payload":"invalid base64"}\n'
          exit
        fi
        jq -n --arg digest "$DIGEST" --arg commit "$COMMIT" --arg scenario "$SCENARIO" '
          {_type:"https://in-toto.io/Statement/v1",predicateType:"https://slsa.dev/provenance/v1",
           subject:[{name:"ghcr.io/tektoncd/tekton-kueue",digest:{sha256:$digest}}],
           predicate:{buildDefinition:{resolvedDependencies:[
             {uri:"git+https://github.com/tektoncd/tekton-kueue.git@refs/tags/v0.4.3",digest:{sha1:$commit}}]}}}
          | if $scenario == "bad-source" then .predicate.buildDefinition.resolvedDependencies[0].uri = "https://github.com/tektoncd/tekton-kueue-evil"
            elif $scenario == "bad-commit" then .predicate.buildDefinition.resolvedDependencies[0].digest.sha1 = ("f" * 40)
            elif $scenario == "bad-digest" then .subject[0].digest.sha256 = ("b" * 64)
            elif $scenario == "bad-predicate" then .predicateType = "https://slsa.dev/provenance/v0.2"
            elif $scenario == "build-config-source" then
              .predicate.buildDefinition.externalParameters.buildConfigSource =
                {repository:"git+https://github.com/tektoncd/tekton-kueue",ref:("sha1:" + $commit),path:"tekton/release-pipeline.yaml"}
              | del(.predicate.buildDefinition.resolvedDependencies)
            else . end
          | if $scenario == "multiple-attestations" then
              (.subject[0].digest.sha256 = ("b" * 64)), .
            else . end
          | {payloadType:"application/vnd.in-toto+json",payload:(tojson | @base64),signatures:[{sig:"verified-by-fake-cosign"}]}
          | if $scenario == "array-attestations" then [.] else . end'
        ;;
      *) exit 90 ;;
    esac
    ;;
  *) exit 90 ;;
esac
EOF
chmod +x "$work/bin/fake"
for executable in gh curl crane cosign; do
  ln -s fake "$work/bin/$executable"
done

run_verifier() {
  bash "$root/hack/verify-release-artifacts.sh" \
    "$VERSION" "$COMMIT" 123 "$work/key.pub" "$work/output"
}

passed=0
for SCENARIO in success published build-config-source multiple-attestations array-attestations \
  checksum traversal absolute extra-checksum duplicate-checksum missing-checksum empty-checksum \
  bucket-release.yaml bucket-release.notags.yaml bucket-SHA256SUMS \
  malformed-metadata metadata-id metadata-tag metadata-commit metadata-draft metadata-assets \
  duplicate-asset missing-asset unsafe-asset-id duplicate-asset-id \
  wrong-image-repository inconsistent-image missing-webhook wrong-deployment tag-in-notags \
  missing-platform duplicate-platform wrong-platform bad-source-label bad-url-label bad-config-platform \
  signature-failure empty-signature attestation-failure empty-attestation malformed-attestation \
  bad-source bad-commit bad-digest bad-predicate; do
  export SCENARIO
  rm -rf "$work/output"
  write_artifacts
  status=0
  run_verifier > "$work/log" 2>&1 || status=$?
  case "$SCENARIO" in
    success|published|build-config-source|multiple-attestations|array-attestations)
      if [[ $status != 0 ]]; then cat "$work/log" >&2; exit 1; fi
      for file in release.yaml release.notags.yaml SHA256SUMS; do
        cmp "$FIXTURES/$file" "$work/output/$file"
      done
      jq -e --arg commit "$COMMIT" --arg image "$IMAGE" --arg scenario "$SCENARIO" \
        --arg version "$VERSION" --arg digest "sha256:$DIGEST" '
        . == {release_id:123,version:$version,commit:$commit,image:$image,digest:$digest,
          draft:($scenario != "published"),assets:[
            {name:"SHA256SUMS",id:13},{name:"release.notags.yaml",id:12},
            {name:"release.yaml",id:11}]}' "$work/output/release-info.json" >/dev/null
      jq -e --arg digest "$DIGEST" '
        [.[] | @base64d | fromjson
          | select(any(.subject[]; .digest.sha256 == $digest))]
        | length > 0' "$work/output/verified-payloads.json" >/dev/null
      ;;
    *)
      [[ $status != 0 && ! -e $work/output/release-info.json ]] || {
        echo "unexpected verification success: $SCENARIO" >&2; exit 1;
      }
      ;;
  esac
  passed=$((passed + 1))
done

export SCENARIO=success
expect_failure() {
  if "$@" > "$work/log" 2>&1; then
    echo "unexpected boundary acceptance" >&2
    exit 1
  fi
  passed=$((passed + 1))
}
verifier="$root/hack/verify-release-artifacts.sh"
rm -rf "$work/output"
expect_failure bash "$verifier"
expect_failure bash "$verifier" ../v0.4.3 "$COMMIT" 123 "$work/key.pub" "$work/output"
expect_failure bash "$verifier" "$VERSION" "${COMMIT^^}" 123 "$work/key.pub" "$work/output"
expect_failure bash "$verifier" "$VERSION" "$COMMIT" ../123 "$work/key.pub" "$work/output"
expect_failure bash "$verifier" "$VERSION" "$COMMIT" 123 "$work/missing.pub" "$work/output"
expect_failure bash "$verifier" "$VERSION" "$COMMIT" 123 "$work/key.pub" ""
expect_failure env GH_TOKEN= bash "$verifier" "$VERSION" "$COMMIT" 123 "$work/key.pub" "$work/output"
expect_failure env GITHUB_REPOSITORY=../repo bash "$verifier" "$VERSION" "$COMMIT" 123 "$work/key.pub" "$work/output"
touch "$work/empty.pub"
expect_failure bash "$verifier" "$VERSION" "$COMMIT" 123 "$work/empty.pub" "$work/output"
printf 'not a public key\n' > "$work/invalid.pub"
expect_failure bash "$verifier" "$VERSION" "$COMMIT" 123 "$work/invalid.pub" "$work/output"
mkdir "$work/output"
printf 'existing artifact\n' > "$work/output/release.yaml"
expect_failure run_verifier
[[ $(< "$work/output/release.yaml") == "existing artifact" ]]
echo "PASS $passed release artifact verification cases"
