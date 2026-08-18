#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -ne 2 ]; then
  echo "usage: $0 pull-requests.json release-notes.md" >&2
  exit 2
fi

: "${RELEASE_SHA:?RELEASE_SHA is required}"
: "${RELEASE_BASE_BRANCH:?RELEASE_BASE_BRANCH is required}"

jq -er \
  --arg sha "$RELEASE_SHA" \
  --arg base "$RELEASE_BASE_BRANCH" \
  '
    [.[] | select(
      .merged_at != null
      and .base.ref == $base
      and .merge_commit_sha == $sha
    )]
    | if length != 1 then
        error("expected exactly one merged pull request for \($sha) into \($base), found \(length)")
      else
        .[0]
        | "## Pull request #\(.number): \(.title)\n\n\(.body // "_No description provided._")\n\n- Author: @\(.user.login)\n- Link: \(.html_url)\n"
      end
  ' "$1" > "$2"
