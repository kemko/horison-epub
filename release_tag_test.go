package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestReleaseTagScriptUsesUniqueRunNumbersAndIsIdempotent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("release workflow runs on Linux")
	}

	for runNumber, want := range map[string]string{"41": "v0.1.41", "42": "v0.1.42"} {
		if got := runReleaseTagName(t, map[string]string{
			"GITHUB_RUN_NUMBER": runNumber,
		}); got != want {
			t.Fatalf("run number %s: got tag %q, want %q", runNumber, got, want)
		}
	}

	mockDir := t.TempDir()
	stateFile := filepath.Join(mockDir, "tag.sha")
	mockGH := filepath.Join(mockDir, "gh")
	const mock = `#!/bin/sh
set -eu
state="$MOCK_GH_STATE"
method=GET
endpoint=
while [ "$#" -gt 0 ]; do
  case "$1" in
    --method) method="$2"; shift 2 ;;
    --jq|--header|-f) shift 2 ;;
    repos/*) endpoint="$1"; shift ;;
    *) shift ;;
  esac
done
if [ "$method" = POST ]; then
  printf '%s\n' "$MOCK_GH_SHA" > "$state"
  exit 0
fi
if [ ! -s "$state" ]; then
  exit 1
fi
sha=$(sed -n '1p' "$state")
printf 'commit %s\n' "$sha"
`
	if err := os.WriteFile(mockGH, []byte(mock), 0o600); err != nil {
		t.Fatal(err)
	}

	env := map[string]string{
		"GITHUB_REPOSITORY": "example/horizont-epub",
		"GITHUB_RUN_NUMBER": "41",
		"GITHUB_SHA":        "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"RELEASE_GH_SCRIPT": mockGH,
		"MOCK_GH_SHA":       "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"MOCK_GH_STATE":     stateFile,
	}
	if err := runReleaseTagEnsure(t, env); err != nil {
		t.Fatalf("first tag creation failed: %v", err)
	}
	if err := runReleaseTagEnsure(t, env); err != nil {
		t.Fatalf("idempotent tag creation failed: %v", err)
	}

	env["GITHUB_SHA"] = strings.Repeat("b", 40)
	if err := runReleaseTagEnsure(t, env); err == nil {
		t.Fatal("tag unexpectedly accepted a different SHA")
	}
}

func runReleaseTagName(t *testing.T, extraEnv map[string]string) string {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "bash", "scripts/release-tag.sh", "tag")
	cmd.Env = releaseTagEnv(extraEnv)
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("release-tag.sh tag: %v", err)
	}
	return strings.TrimSpace(string(output))
}

func runReleaseTagEnsure(t *testing.T, extraEnv map[string]string) error {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "bash", "scripts/release-tag.sh", "ensure")
	cmd.Env = releaseTagEnv(extraEnv)
	return cmd.Run()
}

func releaseTagEnv(extra map[string]string) []string {
	env := os.Environ()
	for key, value := range extra {
		entry := key + "=" + value
		prefix := key + "="
		found := false
		for index, existing := range env {
			if strings.HasPrefix(existing, prefix) {
				env[index] = entry
				found = true
			}
		}
		if !found {
			env = append(env, entry)
		}
	}
	return env
}
