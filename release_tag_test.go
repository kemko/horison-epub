package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestReleaseTagScriptUsesUniqueRunNumbersAndIsIdempotent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("release workflow runs on Linux")
	}

	for runNumber, want := range map[string]string{"41": "v0.1.41", "42": "v0.1.42"} {
		if got := runReleaseTagName(t, map[string]string{
			"GITHUB_RUN_NUMBER":  "9001",
			"RELEASE_RUN_NUMBER": runNumber,
		}); got != want {
			t.Fatalf("run number %s: got tag %q, want %q", runNumber, got, want)
		}
	}

	mockDir := t.TempDir()
	stateFile := filepath.Join(mockDir, "tag.sha")
	mockExecutableDir, err := filepath.Abs("testdata")
	if err != nil {
		t.Fatal(err)
	}

	env := map[string]string{
		"GITHUB_REPOSITORY":  "example/horizont-epub",
		"RELEASE_RUN_NUMBER": "41",
		"PATH":               mockExecutableDir + string(os.PathListSeparator) + os.Getenv("PATH"),
		"RELEASE_SHA":        "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"MOCK_GH_SHA":        "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"MOCK_GH_STATE":      stateFile,
	}
	if err := runReleaseTagEnsure(t, env); err != nil {
		t.Fatalf("first tag creation failed: %v", err)
	}
	if err := runReleaseTagEnsure(t, env); err != nil {
		t.Fatalf("idempotent tag creation failed: %v", err)
	}

	env["RELEASE_SHA"] = strings.Repeat("b", 40)
	if err := runReleaseTagEnsure(t, env); err == nil {
		t.Fatal("tag unexpectedly accepted a different SHA")
	}

	env["MOCK_GH_STATE"] = filepath.Join(mockDir, "raced-tag.sha")
	env["RELEASE_SHA"] = strings.Repeat("c", 40)
	env["MOCK_GH_SHA"] = env["RELEASE_SHA"]
	env["MOCK_GH_POST_FAIL"] = "1"
	if err := runReleaseTagEnsure(t, env); err != nil {
		t.Fatalf("matching tag created during POST race was rejected: %v", err)
	}

	env["MOCK_GH_STATE"] = filepath.Join(mockDir, "mismatched-raced-tag.sha")
	env["RELEASE_SHA"] = strings.Repeat("d", 40)
	env["MOCK_GH_SHA"] = strings.Repeat("e", 40)
	if err := runReleaseTagEnsure(t, env); err == nil {
		t.Fatal("POST race unexpectedly accepted a tag with a different SHA")
	}
}

func TestReleaseNotesRequireOneExactMergedPullRequest(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is required by the release workflow")
	}
	const sha = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	exact := `{"number":7,"title":"Release $(false)","body":null,"merged_at":"2026-08-19T00:00:00Z","merge_commit_sha":"` + sha + `","base":{"ref":"master"},"user":{"login":"author"},"html_url":"https://example.test/pr/7"}`
	wrongBase := strings.Replace(exact, `"master"`, `"other"`, 1)
	wrongSHA := strings.Replace(exact, sha, strings.Repeat("b", 40), 1)

	for _, test := range []struct {
		name    string
		body    string
		wantErr bool
	}{
		{name: "none", body: `[]`, wantErr: true},
		{name: "wrong base and SHA", body: `[` + wrongBase + `,` + wrongSHA + `]`, wantErr: true},
		{name: "one exact", body: `[` + wrongBase + `,` + exact + `]`},
		{name: "multiple exact", body: `[` + exact + `,` + exact + `]`, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := filepath.Join(t.TempDir(), "pull-requests.json")
			output := filepath.Join(t.TempDir(), "release-notes.md")
			if err := os.WriteFile(input, []byte(test.body), 0o600); err != nil {
				t.Fatal(err)
			}
			err := runReleaseNotes(t, input, output, map[string]string{
				"RELEASE_BASE_BRANCH": "master",
				"RELEASE_SHA":         sha,
			})
			if test.wantErr {
				if err == nil {
					t.Fatal("release notes unexpectedly succeeded")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			notes := readTestFile(t, output)
			for _, want := range []string{"Pull request #7", "Release $(false)", "_No description provided._", "@author", "https://example.test/pr/7"} {
				if !strings.Contains(string(notes), want) {
					t.Errorf("release notes do not contain %q: %s", want, notes)
				}
			}
		})
	}
}

func TestWorkflowsPinChecksAndReleaseOnlyAfterSuccessfulPushCI(t *testing.T) {
	ci := string(readTestFile(t, ".github/workflows/ci.yml"))
	for _, want := range []string{
		"github.com/rhysd/actionlint/cmd/actionlint@v1.7.12",
		"golang.org/x/vuln/cmd/govulncheck@v1.1.4",
		"actionlint .github/workflows/ci.yml .github/workflows/release.yml",
		"group: ci-${{ github.event.pull_request.number || github.run_id }}",
		"cancel-in-progress: ${{ github.event_name == 'pull_request' }}",
		"runs-on: windows-2022",
		"run: go test ./...",
	} {
		if !strings.Contains(ci, want) {
			t.Errorf("CI workflow does not contain %q", want)
		}
	}
	if strings.Contains(ci, "govulncheck-action") || strings.Contains(ci, "@latest") {
		t.Fatal("CI workflow uses an unpinned govulncheck path")
	}
	if strings.Contains(ci, "github.event.pull_request.number || github.ref") {
		t.Fatal("CI workflow can cancel an earlier master push before release")
	}

	release := string(readTestFile(t, ".github/workflows/release.yml"))
	for _, want := range []string{
		"workflow_run:",
		"github.event.workflow_run.event == 'push'",
		"github.event.workflow_run.conclusion == 'success'",
		"ref: ${{ github.event.workflow_run.head_sha }}",
		"group: release-${{ github.event.workflow_run.id }}",
		"RELEASE_RUN_NUMBER: ${{ github.event.workflow_run.run_number }}",
	} {
		if !strings.Contains(release, want) {
			t.Errorf("release workflow does not contain %q", want)
		}
	}
	if strings.Contains(release, "\nconcurrency:") {
		t.Fatal("release workflow concurrency can discard pending releases")
	}

	goreleaser := string(readTestFile(t, ".goreleaser.yml"))
	for _, want := range []string{"make_latest: legacy", "replace_existing_artifacts: true"} {
		if !strings.Contains(goreleaser, want) {
			t.Errorf("GoReleaser config does not contain %q", want)
		}
	}
}

func TestDependabotUpdatesSHAPinnedActions(t *testing.T) {
	config := string(readTestFile(t, ".github/dependabot.yml"))
	gomod := strings.Index(config, "package-ecosystem: gomod")
	actions := strings.Index(config, "package-ecosystem: github-actions")
	if gomod < 0 || actions < 0 || gomod >= actions {
		t.Fatal("Dependabot config does not contain both ecosystems")
	}
	if !strings.Contains(config[gomod:actions], "open-pull-requests-limit: 0") {
		t.Fatal("Go module version updates are not disabled")
	}
	if strings.Contains(config[actions:], "open-pull-requests-limit: 0") {
		t.Fatal("SHA-pinned GitHub Actions version updates are disabled")
	}
}

func runReleaseTagName(t *testing.T, extraEnv map[string]string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "scripts/release-tag.sh", "tag")
	cmd.Env = releaseTagEnv(extraEnv)
	output, err := cmd.Output()
	if ctx.Err() != nil {
		t.Fatalf("release-tag.sh tag timed out: %v", ctx.Err())
	}
	if err != nil {
		t.Fatalf("release-tag.sh tag: %v", err)
	}
	return strings.TrimSpace(string(output))
}

func runReleaseTagEnsure(t *testing.T, extraEnv map[string]string) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "scripts/release-tag.sh", "ensure")
	cmd.Env = releaseTagEnv(extraEnv)
	err := cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("release-tag.sh ensure timed out: %v", ctx.Err())
	}
	return err
}

func runReleaseNotes(t *testing.T, input, output string, extraEnv map[string]string) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "-c", `bash scripts/release-notes.sh "$TEST_INPUT" "$TEST_OUTPUT"`)
	extraEnv["TEST_INPUT"] = input
	extraEnv["TEST_OUTPUT"] = output
	cmd.Env = releaseTagEnv(extraEnv)
	err := cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("release-notes.sh timed out: %v", ctx.Err())
	}
	return err
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
