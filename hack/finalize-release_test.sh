#!/usr/bin/env bash
set -euo pipefail

# Copies of this file provide only the separately tested verifier/smoke and
# network executable boundaries. The finalizer itself is copied without edits.
if [[ ${FINALIZER_TEST_CHILD:-} == true ]]; then
  command=${0##*/}
  event() {
    jq -cn --arg event "$1" --arg token "${GH_TOKEN:-}" --args \
      '{event:$event,token:$token,args:$ARGS.positional}' -- "${@:2}" >> "$CALLS"
  }
  case "$command" in
    verify-release-artifacts.sh)
      event verify "$@"
      [[ $# == 5 && $1 == "$VERSION" && $2 == "$COMMIT" && $3 == 123 &&
         $4 == "$CASE_DIR/key.pub" && $5 == "$CASE_DIR/work/verified" ]]
      openssl pkey -pubin -in "$4" -noout
      [[ $MODE != verifier-failure ]] || exit 42
      mkdir -p "$5"
      cp "$CASE_DIR"/{release.yaml,release.notags.yaml,SHA256SUMS,verified-payloads.json} "$5/"
      jq --arg image "$IMAGE" --arg digest "$DIGEST" --arg version "$VERSION" \
        --arg commit "$COMMIT" --arg mode "$MODE" '
        {release_id:.id,version:$version,commit:$commit,image:$image,
         digest:(if $mode == "bad-digest" then "sha256:" + ("b"*64) else $digest end),
         draft:.draft,assets:(.assets | sort_by(.name))}
      ' "$CASE_DIR/original.json" > "$5/release-info.json"
      ;;
    smoke-release.sh)
      event smoke "$@"
      [[ $# == 3 && $1 == "$CASE_DIR/work/verified/release.yaml" &&
         $2 == "$IMAGE" && $3 == "$CASE_DIR/work/smoke" ]]
      cmp "$1" "$CASE_DIR/release.yaml"
      [[ $MODE != smoke-failure ]] || exit 42
      mkdir "$3"
      cp "$1" "$3/release.yaml"
      printf '%s\n' "$2" > "$3/expected-image.txt"
      ;;
    gh)
      [[ $1 == api ]]
      shift
      if [[ $1 == user ]]; then
        [[ $# == 3 && $2 == --jq && $3 == .login ]]
        event identity "$@"
        if [[ $MODE == wrong-actor ]]; then echo waveywaves; else echo tekton-robot; fi
      elif [[ $1 == --method ]]; then
        [[ $# == 5 && $2 == PATCH &&
           $3 == repos/tektoncd/tekton-kueue/releases/123 && $4 == --input ]]
        event patch "$@"
        [[ $GH_TOKEN == test-bot-token ]]
        cp "$5" "$CASE_DIR/sent-patch.json"
        [[ $MODE != api-failure ]] || exit 42
        jq -s '.[0] * (.[1] | del(.make_latest))' \
          "$CASE_DIR/release.json" "$5" > "$CASE_DIR/updated.json"
        if [[ $MODE == wrong-result ]]; then
          jq '.draft = true' "$CASE_DIR/updated.json" > "$CASE_DIR/release.json"
        else
          cp "$CASE_DIR/updated.json" "$CASE_DIR/release.json"
        fi
        if [[ $(jq -r .make_latest "$5") == true && $MODE != wrong-latest ]]; then
          printf '%s\n' "$VERSION" > "$CASE_DIR/latest"
        fi
        cat "$CASE_DIR/release.json"
      else
        case "$1" in
          repos/tektoncd/tekton-kueue/environments/kueue-release-finalization)
            [[ $# == 1 ]]
            event environment
            cat "$CASE_DIR/environment.json"
            ;;
          repos/tektoncd/tekton-kueue/releases/latest)
            [[ $# == 3 && $2 == --jq && $3 == .tag_name ]]
            event latest
            cat "$CASE_DIR/latest"
            ;;
          repos/tektoncd/tekton-kueue/releases/123)
            [[ $# == 1 ]]
            event release
            cat "$CASE_DIR/release.json"
            ;;
          *) exit 90 ;;
        esac
      fi
      ;;
    curl)
      if [[ $# == 6 && $1 == -fsS && $2 == -H &&
            $3 == 'Content-Type: application/json' && $4 == -d &&
            $6 == https://rekor.sigstore.dev/api/v1/index/retrieve ]]; then
        jq -e --arg digest "$DIGEST" '.hash == $digest' <<< "$5" >/dev/null
        event rekor-index
        printf '["abcdef"]\n'
      elif [[ $# == 2 && $1 == -fsS &&
              $2 == https://rekor.sigstore.dev/api/v1/log/entries/abcdef ]]; then
        event rekor-entry
        hash=$(openssl dgst -sha256 -r "$CASE_DIR/payload.json")
        hash=${hash%% *}
        [[ $MODE != rekor-mismatch ]] || hash=${hash//?/0}
        jq -cn --arg hash "$hash" \
          '{abcdef:{body:({kind:"dsse",spec:{payloadHash:{value:$hash}}}|tojson|@base64)}}'
      elif [[ $# == 4 && $1 == -fsSL && $3 == --output ]]; then
        prefix=https://infra.tekton.dev/tekton-releases/tekton-kueue/latest/
        [[ $2 == "$prefix"* ]]
        file=${2#"$prefix"}
        case "$file" in release.yaml|release.notags.yaml|SHA256SUMS) ;; *) exit 90 ;; esac
        event bucket-read "$file"
        cp "$CASE_DIR/$file" "$4"
        [[ $MODE != bucket-failure ]] || printf 'changed\n' >> "$4"
      else exit 90; fi
      ;;
    crane)
      [[ $DOCKER_CONFIG == "$CASE_DIR/work/docker" && -d $DOCKER_CONFIG ]]
      case "$1" in
        auth)
          [[ $# == 6 && $2 == login && $3 == ghcr.io &&
             $4 == --username && $5 == tekton-robot && $6 == --password-stdin ]]
          token=$(cat)
          [[ $token == test-registry-token ]]
          event login "$@"
          printf '{}\n' > "$DOCKER_CONFIG/config.json"
          ;;
        cp)
          [[ $# == 3 && $2 == "$IMAGE" && $3 == ghcr.io/tektoncd/tekton-kueue:latest ]]
          event image-copy "$@"
          ;;
        digest)
          [[ $# == 2 && $2 == ghcr.io/tektoncd/tekton-kueue:latest ]]
          event image-digest "$@"
          if [[ $MODE == image-failure ]]; then printf 'sha256:%064d\n' 0; else echo "$DIGEST"; fi
          ;;
        *) exit 90 ;;
      esac
      ;;
    oci)
      [[ $1 == --config-file && $2 == "$CASE_DIR/work/oci/config" ]]
      [[ -s $2 && -s $CASE_DIR/work/oci/key.pem ]]
      if [[ $(uname -s) == Darwin ]]; then mode=(-f %Lp); else mode=(-c %a); fi
      [[ $(stat "${mode[@]}" "$2") == 600 ]]
      [[ $(stat "${mode[@]}" "$CASE_DIR/work/oci") == 700 ]]
      shift 2
      if [[ $* == 'os ns get --query data --raw-output' ]]; then
        event namespace "$@"
        echo test-namespace
      elif [[ $# == 12 && $1 == os && $2 == object && $3 == put &&
              $4 == --namespace-name && $5 == test-namespace &&
              $6 == --bucket-name && $7 == tekton-releases && $8 == --name &&
              ${10} == --file && ${12} == --force ]]; then
        file=${9#tekton-kueue/latest/}
        case "$file" in release.yaml|release.notags.yaml|SHA256SUMS) ;; *) exit 90 ;; esac
        [[ $9 == "tekton-kueue/latest/$file" && ${11} == "$CASE_DIR/work/verified/$file" ]]
        cmp "${11}" "$CASE_DIR/$file"
        event bucket-put "$file"
      else exit 90; fi
      ;;
    *) exit 90 ;;
  esac
  exit 0
fi

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
tmp=$(mktemp -d)
trap 'status=$?; if [[ $status == 0 ]]; then rm -rf "$tmp"; else echo "test artifacts: $tmp" >&2; fi' EXIT
mkdir -p "$tmp/checkout/hack" "$tmp/bin"
mkdir "$tmp/caller-docker"
printf 'caller configuration\n' > "$tmp/caller-docker/config.json"
cp "$tmp/caller-docker/config.json" "$tmp/caller-config-original"
export DOCKER_CONFIG="$tmp/caller-docker"
cp "$root/hack/finalize-release.sh" "$tmp/checkout/hack/"
for name in verify-release-artifacts.sh smoke-release.sh; do
  cp "${BASH_SOURCE[0]}" "$tmp/checkout/hack/$name"
done
for name in gh curl crane oci; do
  cp "${BASH_SOURCE[0]}" "$tmp/bin/$name"
  chmod +x "$tmp/bin/$name"
done
export PATH="$tmp/bin:$PATH" FINALIZER_TEST_CHILD=true
export GITHUB_REPOSITORY=tektoncd/tekton-kueue GITHUB_REF=refs/heads/main
export GH_TOKEN=test-bot-token GHCR_TOKEN=test-registry-token
export OCI_API_KEY=test-private-key OCI_FINGERPRINT=test-fingerprint
export OCI_TENANCY_OCID=test-tenancy OCI_USER_OCID=test-user OCI_REGION=test-region
export VERSION=v0.4.3 COMMIT=0123456789abcdef0123456789abcdef01234567
export DIGEST=sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
export IMAGE="ghcr.io/tektoncd/tekton-kueue@$DIGEST"
export CASE_DIR CALLS MODE
cases=0
check() {
  jq -e -s "$1" "$CALLS" >/dev/null || {
    echo "failed event assertion: $MODE: $1" >&2; exit 1;
  }
}
run_case() {
  MODE=$1
  CASE_DIR="$tmp/$MODE"
  CALLS="$CASE_DIR/calls.jsonl"
  mkdir "$CASE_DIR"
  : > "$CALLS"
  cat > "$CASE_DIR/key.pub" <<'EOF'
-----BEGIN PUBLIC KEY-----
MCowBQYDK2VwAyEA11qYAYKxCrfVS/7TyWQHOg7hcvPapiMlrwIaaPcHURo=
-----END PUBLIC KEY-----
EOF
  local operation=finalize latest=false version=$VERSION ref=refs/heads/main
  case "$MODE" in
    success|older-latest|image-failure|bucket-failure|wrong-latest) latest=true ;;
    repair-*) operation=repair ;;
    invalid-input) version=../v0.4.3 ;;
    bad-ref) ref=refs/heads/untrusted ;;
  esac
  printf 'v0.4.2\n' > "$CASE_DIR/latest"
  [[ $MODE != older-latest ]] || printf 'v0.5.0\n' > "$CASE_DIR/latest"
  [[ $MODE != repair-published ]] || printf '%s\n' "$VERSION" > "$CASE_DIR/latest"
  jq -n --arg mode "$MODE" '{
    can_admins_bypass:($mode == "admin-bypass"),
    protection_rules:(if $mode == "unprotected" then [] else
      [{type:"required_reviewers",prevent_self_review:($mode != "self-review"),
        reviewers:[{type:"Team",reviewer:{id:18766447}}]}] end)}' > "$CASE_DIR/environment.json"
  jq -n --arg version "$VERSION" --arg commit "$COMMIT" --arg mode "$MODE" '{
    id:123,tag_name:$version,target_commitish:$commit,draft:($mode != "repair-published"),
    body:(if $mode == "generated-notes" then "Generated release notes\n"
      else "Release notes\n\n## Attestation\nold\n\n<!-- Any special upgrade notice -->\nKeep this\n" end),
    assets:[{name:"release.yaml",id:11},{name:"release.notags.yaml",id:12},{name:"SHA256SUMS",id:13}]}
  ' > "$CASE_DIR/original.json"
  jq --arg mode "$MODE" '
    if $mode == "wrong-source" then .target_commitish = "main"
    elif $mode == "changed-assets" then .assets[0].id = 99
    else . end' "$CASE_DIR/original.json" > "$CASE_DIR/release.json"
  printf 'image: %s\n' "$IMAGE" > "$CASE_DIR/release.yaml"
  cp "$CASE_DIR/release.yaml" "$CASE_DIR/release.notags.yaml"
  (cd "$CASE_DIR"; openssl dgst -sha256 -r release.yaml release.notags.yaml) > "$CASE_DIR/SHA256SUMS"
  # Hash exactly the decoded verified bytes, not the encoded envelope or JSON
  # pretty-printing. Both finalizer and Rekor fixture use real SHA-256.
  jq -cn --arg commit "$COMMIT" --arg digest "${DIGEST#sha256:}" '{
    _type:"https://in-toto.io/Statement/v1",predicateType:"https://slsa.dev/provenance/v1",
    subject:[{name:"ghcr.io/tektoncd/tekton-kueue",digest:{sha256:$digest}}],
    predicate:{buildDefinition:{resolvedDependencies:[{
      uri:"git+https://github.com/tektoncd/tekton-kueue.git",digest:{sha1:$commit}}]}}}
  ' > "$CASE_DIR/payload.json"
  jq -n --rawfile payload "$CASE_DIR/payload.json" '[$payload | @base64]' > "$CASE_DIR/verified-payloads.json"
  local status=0
  GITHUB_REF=$ref bash "$tmp/checkout/hack/finalize-release.sh" \
    "$operation" "$version" "$COMMIT" 123 "$DIGEST" "$CASE_DIR/key.pub" "$latest" \
    "$CASE_DIR/work" > "$CASE_DIR/output" 2>&1 || status=$?
  if [[ $status == 90 ]]; then
    echo "unexpected executable boundary: $MODE; see $CASE_DIR/output" >&2; exit 1;
  fi
  case "$MODE" in
    success|repair-draft|repair-published|generated-notes)
      [[ $status == 0 ]] || { echo "failed $MODE; see $CASE_DIR/output" >&2; exit 1; }
      local expected_latest=$latest
      [[ $MODE != repair-published ]] || expected_latest=true
      jq -e --arg version "$VERSION" --arg commit "$COMMIT" --arg latest "$expected_latest" \
        --arg operation "$operation" --slurpfile original "$CASE_DIR/original.json" '
        .tag_name == $version and .target_commitish == $commit and .make_latest == $latest
        and .draft == (if $operation == "repair" then $original[0].draft else false end)
        and (if $original[0].body | contains("## Attestation") then
          (.body | startswith("Release notes\n\n") and endswith("<!-- Any special upgrade notice -->\nKeep this\n"))
          else .body | startswith($original[0].body) end)
        and (.body | contains("abcdef"))' "$CASE_DIR/sent-patch.json" >/dev/null
      jq -e -s '.[0] == (.[1] * (.[2] | del(.make_latest)))' \
        "$CASE_DIR/release.json" "$CASE_DIR/original.json" "$CASE_DIR/sent-patch.json" >/dev/null
      check 'map(.event) as $e | ($e|index("verify")) < ($e|index("rekor-entry"))
        and ($e|index("rekor-entry")) < ($e|index("release"))
        and ($e|index("release")) < ($e|index("patch"))
        and all(.[] | select(.event == "patch"); .token == "test-bot-token")
        and ([.[] | select(.event == "identity")][-1].token == "test-bot-token")'
      if [[ $operation == repair ]]; then
        check 'all(.[]; .event != "smoke" and .event != "image-copy" and .event != "bucket-put" and .event != "login")'
      elif [[ $latest == true ]]; then
        check 'map(.event) as $e | ($e|index("verify")) < ($e|index("smoke"))
          and ($e|index("smoke")) < ($e|index("image-copy"))
          and ($e|index("image-copy")) < ($e|index("image-digest"))
          and ($e|index("image-digest")) < ($e|index("bucket-put"))
          and ($e|rindex("bucket-read")) < ($e|index("patch"))
          and ([.[] | select(.event == "bucket-put") | .args[0]] | sort) ==
            ["SHA256SUMS","release.notags.yaml","release.yaml"]'
      else
        check 'map(.event) as $e | ($e|index("verify")) < ($e|index("smoke"))
          and ($e|index("smoke")) < ($e|index("patch"))
          and all(.[]; .event != "image-copy" and .event != "bucket-put" and .event != "login")'
      fi
      ;;
    *)
      [[ $status != 0 ]] || { echo "unexpected success: $MODE" >&2; exit 1; }
      case "$MODE" in
        verifier-failure|smoke-failure|api-failure) [[ $status == 42 ]] ;;
        *) [[ $status == 1 ]] ;;
      esac
      case "$MODE" in
        api-failure|wrong-result|wrong-latest)
          check 'any(.[]; .event == "patch")'
          [[ $MODE != api-failure ]] || cmp "$CASE_DIR/original.json" "$CASE_DIR/release.json"
          ;;
        *)
          check 'all(.[]; .event != "patch")'
          cmp "$CASE_DIR/release.json" "$CASE_DIR/work/current-release.json" 2>/dev/null ||
            [[ ! -e $CASE_DIR/work/current-release.json ]]
          ;;
      esac
      case "$MODE" in
        image-failure|bucket-failure)
          check 'any(.[]; .event == "image-copy")'
          cmp "$CASE_DIR/original.json" "$CASE_DIR/release.json"
          ;;
        api-failure|wrong-result|wrong-latest) ;;
        *)
          check 'all(.[]; .event != "image-copy" and .event != "bucket-put" and .event != "login")'
          ;;
      esac
      case "$MODE" in
        wrong-actor|unprotected|self-review|admin-bypass|older-latest|invalid-input|bad-ref)
          check 'all(.[]; .event != "verify" and .event != "smoke")' ;;
        verifier-failure|bad-digest)
          check 'any(.[]; .event == "verify") and all(.[]; .event != "smoke")' ;;
        smoke-failure) check 'any(.[]; .event == "smoke")' ;;
        rekor-mismatch) check 'any(.[]; .event == "rekor-entry")' ;;
        wrong-source|changed-assets) check 'any(.[]; .event == "release")' ;;
        image-failure)
          check 'any(.[]; .event == "image-digest") and all(.[]; .event != "bucket-put")' ;;
        bucket-failure) check 'any(.[]; .event == "bucket-read")' ;;
      esac
      ;;
  esac
  [[ ! -e $CASE_DIR/work/docker && ! -e $CASE_DIR/work/oci ]]
  cmp "$tmp/caller-docker/config.json" "$tmp/caller-config-original"
  cases=$((cases + 1))
  echo "PASS $MODE"
}

# Fifteen focused groups; related protection, repair and API outcomes share a
# group rather than multiplying every failure across finalize/repair/latest.
run_case success
run_case generated-notes
for mode in repair-draft repair-published; do run_case "$mode"; done
run_case wrong-actor
for mode in unprotected self-review admin-bypass; do run_case "$mode"; done
run_case bad-digest
run_case verifier-failure
run_case smoke-failure
run_case wrong-source
run_case changed-assets
run_case rekor-mismatch
run_case older-latest
run_case image-failure
run_case bucket-failure
for mode in api-failure wrong-result wrong-latest; do run_case "$mode"; done
for mode in invalid-input bad-ref; do run_case "$mode"; done
echo "PASS $cases deterministic release finalization cases (16 focused groups)"
