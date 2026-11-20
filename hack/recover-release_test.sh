#!/usr/bin/env bash
set -euo pipefail

if [[ ${RECOVERY_TEST_CHILD:-} == true ]]; then
  event() {
    jq -cn --arg event "$1" --args '{event:$event,args:$ARGS.positional}' \
      -- "${@:2}" >> "$CALLS"
  }
  case ${0##*/} in
    verify-release-artifacts.sh)
      [[ $# == 5 && $1 == "$VERSION" && $2 == "$COMMIT" && $4 == "$CASE_DIR/key.pub" ]]
      openssl pkey -pubin -in "$4" -noout
      event verify "$3"
      [[ $MODE != verification-failure && $MODE != incomplete-bundle ]] || exit 42
      [[ $3 == bucket || $3 == 123 ]] || exit 90
      [[ $MODE != final-verification-failure || $3 == bucket ]] || exit 42
      mkdir -p "$5"
      cp "$CASE_DIR"/{release.yaml,release.notags.yaml,SHA256SUMS} "$5/"
      jq -n --arg digest "$DIGEST" --arg mode "$MODE" --arg id "$3" '
        {digest:(if $mode == "wrong-digest" then "sha256:" + ("b"*64) else $digest end),
         draft:($id != "bucket"),release_id:(if $id == "bucket" then null else 123 end)}
      ' > "$5/release-info.json"
      ;;
    gh)
      if [[ $1 == release ]]; then
        [[ $# == 6 && $2 == upload && $3 == "$VERSION" &&
           $5 == --repo && $6 == tektoncd/tekton-kueue ]]
        file=${4##*/}
        cmp "$4" "$CASE_DIR/$file"
        # Mimic the real non-clobber upload contract.
        if jq -e --arg name "$file" 'any(.assets[]; .name == $name)' "$CASE_DIR/state.json" >/dev/null; then
          exit 90
        fi
        [[ $MODE != upload-failure ]] || exit 42
        event upload "$file"
        case $file in release.yaml) id=11 ;; release.notags.yaml) id=12 ;; SHA256SUMS) id=13 ;; *) exit 90 ;; esac
        jq --arg name "$file" --argjson id "$id" '.assets += [{name:$name,id:$id}]' \
          "$CASE_DIR/state.json" > "$CASE_DIR/next.json"
        mv "$CASE_DIR/next.json" "$CASE_DIR/state.json"
        exit
      fi
      [[ $1 == api ]] || exit 90
      shift
      case $1 in
        user)
          [[ $# == 3 && $2 == --jq && $3 == .login ]]
          if [[ $MODE == wrong-actor ]]; then echo waveywaves; else echo tekton-robot; fi
          ;;
        repos/tektoncd/tekton-kueue)
          [[ $# == 3 && $2 == --jq && $3 == .permissions.push ]]
          if [[ $MODE == no-permission ]]; then echo false; else echo true; fi
          ;;
        repos/tektoncd/tekton-kueue/environments/kueue-release-finalization)
          jq -n --arg mode "$MODE" '{can_admins_bypass:($mode == "admin-bypass"),
            protection_rules:(if $mode == "unprotected" then [] else [{
              type:"required_reviewers",prevent_self_review:($mode != "self-review"),
              reviewers:[{type:"Team",reviewer:{id:18766447}}]}] end)}'
          ;;
        --paginate)
          [[ $# == 3 && $2 == --slurp ]] || exit 90
          case $3 in
            repos/tektoncd/tekton-kueue/git/matching-refs/tags/*)
              [[ $MODE != tag-api-failure ]] || exit 42
              if [[ $MODE == no-tag ]]; then echo '[[]]'; else
                jq -n --arg version "$VERSION" --arg commit "$COMMIT" --arg mode "$MODE" '
                  [[{ref:("refs/tags/" + $version),object:{
                    type:(if $mode == "annotated-tag" then "tag" else "commit" end),
                    sha:(if $mode == "wrong-tag" then "f"*40
                      elif $mode == "annotated-tag" then "e"*40 else $commit end)}}]]'
              fi
              ;;
            'repos/tektoncd/tekton-kueue/releases?per_page=100')
              [[ $MODE != lookup-failure ]] || exit 42
              jq --arg mode "$MODE" '
                if . == null then [[]] elif $mode == "duplicate-release" then [[.,.]]
                else [[],[.]] end' "$CASE_DIR/state.json"
              ;;
            *) exit 90 ;;
          esac
          ;;
        repos/tektoncd/tekton-kueue/git/tags/*)
          [[ $MODE == annotated-tag ]]
          jq -n --arg commit "$COMMIT" '{object:{type:"commit",sha:$commit}}'
          ;;
        repos/tektoncd/tekton-kueue/releases/123)
          cat "$CASE_DIR/state.json"
          ;;
        --method)
          if [[ $2 == POST && $3 == repos/tektoncd/tekton-kueue/releases/generate-notes ]]; then
            [[ $# == 7 && $4 == -f && $5 == "tag_name=$VERSION" &&
               $6 == -f && $7 == "target_commitish=$COMMIT" ]]
            [[ $MODE != notes-failure ]] || exit 42
            event notes
            jq -n '{body:"Generated notes from existing commits"}'
          elif [[ $2 == POST && $3 == repos/tektoncd/tekton-kueue/releases ]]; then
            [[ $# == 5 && $4 == --input && $(< "$CASE_DIR/state.json") == null ]]
            jq -e --arg version "$VERSION" --arg commit "$COMMIT" '
              .tag_name == $version and .target_commitish == $commit
              and .draft == true and .make_latest == "false" and (.body | length > 0)
            ' "$5" >/dev/null
            [[ $MODE != create-failure ]] || exit 42
            event create
            jq '. + {id:123,assets:[]}' "$5" > "$CASE_DIR/state.json"
            cat "$CASE_DIR/state.json"
          elif [[ $2 == GET && $3 == -H && $4 == 'Accept: application/octet-stream' ]]; then
            [[ $# == 5 ]]
            case ${5##*/} in 11) file=release.yaml ;; 12) file=release.notags.yaml ;; 13) file=SHA256SUMS ;; *) exit 90 ;; esac
            cat "$CASE_DIR/$file"
            [[ $MODE != conflicting-asset ]] || printf 'conflict\n'
          else exit 90; fi
          ;;
        *) exit 90 ;;
      esac
      ;;
    *) exit 90 ;;
  esac
  exit
fi

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
tmp=$(mktemp -d)
trap 'status=$?; if [[ $status == 0 ]]; then rm -rf "$tmp"; else echo "test artifacts: $tmp" >&2; fi' EXIT
mkdir -p "$tmp/checkout/hack" "$tmp/bin"
cp "$root/hack/recover-release.sh" "$tmp/checkout/hack/"
cp "$root/hack/verify-release-tag.sh" "$tmp/checkout/hack/"
cp "${BASH_SOURCE[0]}" "$tmp/checkout/hack/verify-release-artifacts.sh"
cp "${BASH_SOURCE[0]}" "$tmp/bin/gh"
chmod +x "$tmp/bin/gh"
export PATH="$tmp/bin:$PATH" RECOVERY_TEST_CHILD=true GH_TOKEN=test-robot-token
export GITHUB_REPOSITORY=tektoncd/tekton-kueue GITHUB_REF=refs/heads/main
export VERSION=v0.4.3 COMMIT=0123456789abcdef0123456789abcdef01234567
export DIGEST=sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
export MODE CASE_DIR CALLS
cases=0
for MODE in missing-draft no-tag partial-assets complete annotated-tag \
  wrong-actor no-permission unprotected self-review admin-bypass verification-failure \
  incomplete-bundle wrong-digest wrong-tag tag-api-failure lookup-failure duplicate-release \
  wrong-source published duplicate-asset duplicate-asset-id conflicting-asset notes-failure create-failure \
  upload-failure final-verification-failure invalid-input bad-ref missing-key invalid-key; do
  CASE_DIR="$tmp/$MODE"
  mkdir "$CASE_DIR"
  CALLS="$CASE_DIR/calls.jsonl"
  : > "$CALLS"
  cat > "$CASE_DIR/key.pub" <<'EOF'
-----BEGIN PUBLIC KEY-----
MCowBQYDK2VwAyEA11qYAYKxCrfVS/7TyWQHOg7hcvPapiMlrwIaaPcHURo=
-----END PUBLIC KEY-----
EOF
  [[ $MODE != invalid-key ]] || printf 'not a public key\n' > "$CASE_DIR/key.pub"
  printf 'verified manifest\n' > "$CASE_DIR/release.yaml"
  cp "$CASE_DIR/release.yaml" "$CASE_DIR/release.notags.yaml"
  (cd "$CASE_DIR"; openssl dgst -sha256 -r release.yaml release.notags.yaml) > "$CASE_DIR/SHA256SUMS"
  jq -n --arg version "$VERSION" --arg commit "$COMMIT" --arg mode "$MODE" '
    if (["missing-draft","no-tag","notes-failure","create-failure"] | index($mode)) != null
    then null else
      {id:123,tag_name:$version,
       target_commitish:(if $mode == "wrong-source" then "main" else $commit end),
       draft:($mode != "published"),body:"Keep existing notes",assets:
         (if $mode == "complete" then
           [{name:"release.yaml",id:11},{name:"release.notags.yaml",id:12},{name:"SHA256SUMS",id:13}]
          elif $mode == "duplicate-asset" then [{name:"release.yaml",id:11},{name:"release.yaml",id:14}]
          elif $mode == "duplicate-asset-id" then [{name:"release.yaml",id:11},{name:"release.notags.yaml",id:11}]
          else [{name:"release.yaml",id:11}] end)}
    end' > "$CASE_DIR/state.json"
  cp "$CASE_DIR/state.json" "$CASE_DIR/original.json"
  version=$VERSION
  ref=refs/heads/main
  key="$CASE_DIR/key.pub"
  [[ $MODE != invalid-input ]] || version=../v0.4.3
  [[ $MODE != bad-ref ]] || ref=refs/heads/untrusted
  [[ $MODE != missing-key ]] || key="$CASE_DIR/missing.pub"
  status=0
  GITHUB_REF=$ref bash "$tmp/checkout/hack/recover-release.sh" \
    "$version" "$COMMIT" "$DIGEST" "$key" "$CASE_DIR/work" > "$CASE_DIR/output" 2>&1 || status=$?
  case $MODE in
    missing-draft|no-tag|partial-assets|complete|annotated-tag)
      [[ $status == 0 ]] || { echo "failed $MODE; see $CASE_DIR/output" >&2; exit 1; }
      jq -e '.draft == true and ([.assets[].name] | sort ==
        ["SHA256SUMS","release.notags.yaml","release.yaml"])' "$CASE_DIR/state.json" >/dev/null
      if [[ $MODE != missing-draft && $MODE != no-tag ]]; then
        jq -e -s '.[0].body == .[1].body' "$CASE_DIR/state.json" "$CASE_DIR/original.json" >/dev/null
      fi
      jq -e -s '.[0] == {event:"verify",args:["bucket"]}
        and .[-1] == {event:"verify",args:["123"]}' "$CALLS" >/dev/null
      writes=$(jq -s '[.[] | select(.event == "create" or .event == "upload")] | length' "$CALLS")
      bash "$tmp/checkout/hack/recover-release.sh" "$VERSION" "$COMMIT" "$DIGEST" \
        "$key" "$CASE_DIR/again" >> "$CASE_DIR/output" 2>&1
      [[ $(jq -s '[.[] | select(.event == "create" or .event == "upload")] | length' "$CALLS") == "$writes" ]]
      ;;
    *)
      [[ $status != 0 && $status != 90 ]] || { echo "unexpected $MODE status $status; see $CASE_DIR/output" >&2; exit 1; }
      case $MODE in
        upload-failure|final-verification-failure) ;;
        *) jq -e -s 'all(.[]; .event != "create" and .event != "upload")' "$CALLS" >/dev/null ;;
      esac
      ;;
  esac
  cases=$((cases + 1))
  echo "PASS $MODE"
done
echo "PASS $cases deterministic release recovery cases"
