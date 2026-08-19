#!/usr/bin/env bash
set -euo pipefail

tag_for_run() {
  local tag patch max_patch=0
  while IFS= read -r tag; do
    patch="${tag#v0.1.}"
    case "$patch" in
      ''|*[!0-9]*) continue ;;
    esac
    if [ "$patch" -gt "$max_patch" ]; then
      max_patch="$patch"
    fi
  done < <(git tag --list 'v0.1.*')
  printf 'v0.1.%s\n' "$((max_patch + 1))"
}

tag_sha() {
  local tag="$1"
  local object object_type object_sha
  object="$(gh api "repos/${GITHUB_REPOSITORY}/git/ref/tags/${tag}" \
    --jq '.object.type + " " + .object.sha')" || return 1
  read -r object_type object_sha <<< "$object"
  [ -n "$object_type" ] && [ -n "$object_sha" ] || return 1
  if [ "$object_type" = tag ]; then
    gh api "repos/${GITHUB_REPOSITORY}/git/tags/${object_sha}" --jq '.object.sha'
  else
    printf '%s\n' "$object_sha"
  fi
}

ensure_tag() {
  local tag="$1"
  local expected_sha="${RELEASE_SHA:?RELEASE_SHA is required}"
  local existing_sha

  if existing_sha="$(tag_sha "$tag" 2>/dev/null)"; then
    if [ "$existing_sha" = "$expected_sha" ]; then
      return 0
    fi
    printf 'tag %s already points to %s, expected %s\n' \
      "$tag" "$existing_sha" "$expected_sha" >&2
    return 1
  fi

  if gh api \
    --method POST \
    "repos/${GITHUB_REPOSITORY}/git/refs" \
    -f "ref=refs/tags/${tag}" \
    -f "sha=${expected_sha}" \
    >/dev/null; then
    return 0
  fi

  existing_sha="$(tag_sha "$tag")" || {
    echo "failed to create or read tag $tag" >&2
    return 1
  }
  if [ "$existing_sha" != "$expected_sha" ]; then
    printf 'tag %s already points to %s, expected %s\n' \
      "$tag" "$existing_sha" "$expected_sha" >&2
    return 1
  fi
}

case "${1:-}" in
  tag)
    tag_for_run
    ;;
  ensure)
    [ "$#" -eq 1 ] || { echo "usage: $0 ensure" >&2; exit 2; }
    : "${GITHUB_REPOSITORY:?GITHUB_REPOSITORY is required}"
    ensure_tag "$(tag_for_run)"
    ;;
  *)
    echo "usage: $0 {tag|ensure}" >&2
    exit 2
    ;;
esac
