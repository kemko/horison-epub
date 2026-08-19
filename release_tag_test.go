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

func TestReleaseTagScriptIsNumericAndIdempotent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("release workflow runs on Linux")
	}

	for _, test := range []struct {
		runNumber string
		want      string
		wantErr   bool
	}{
		{runNumber: "41", want: "v0.1.41"},
		{runNumber: "42", want: "v0.1.42"},
		{runNumber: "not-a-number", wantErr: true},
	} {
		output, err := runReleaseScript(t, "scripts/release-tag.sh", []string{"tag"}, map[string]string{
			"RELEASE_RUN_NUMBER": test.runNumber,
		})
		if test.wantErr {
			if err == nil {
				t.Fatalf("run number %q was accepted", test.runNumber)
			}
			continue
		}
		if err != nil {
			t.Fatalf("run number %q: %v: %s", test.runNumber, err, output)
		}
		if got := strings.TrimSpace(string(output)); got != test.want {
			t.Fatalf("run number %q: tag = %q, want %q", test.runNumber, got, test.want)
		}
	}

	mockExecutableDir, err := filepath.Abs("testdata")
	if err != nil {
		t.Fatal(err)
	}
	mockDir := t.TempDir()
	env := map[string]string{
		"GITHUB_REPOSITORY":  "example/horisont-epub",
		"RELEASE_RUN_NUMBER": "41",
		"PATH":               mockExecutableDir + string(os.PathListSeparator) + os.Getenv("PATH"),
		"RELEASE_SHA":        strings.Repeat("a", 40),
		"MOCK_GH_SHA":        strings.Repeat("a", 40),
		"MOCK_GH_STATE":      filepath.Join(mockDir, "tag.sha"),
	}
	for attempt := range 2 {
		if output, err := runReleaseScript(t, "scripts/release-tag.sh", []string{"ensure"}, env); err != nil {
			t.Fatalf("ensure attempt %d: %v: %s", attempt+1, err, output)
		}
	}

	env["RELEASE_SHA"] = strings.Repeat("b", 40)
	if _, err := runReleaseScript(t, "scripts/release-tag.sh", []string{"ensure"}, env); err == nil {
		t.Fatal("existing tag accepted a different SHA")
	}

	env["MOCK_GH_STATE"] = filepath.Join(mockDir, "raced-tag.sha")
	env["RELEASE_SHA"] = strings.Repeat("c", 40)
	env["MOCK_GH_SHA"] = env["RELEASE_SHA"]
	env["MOCK_GH_POST_FAIL"] = "1"
	if output, err := runReleaseScript(t, "scripts/release-tag.sh", []string{"ensure"}, env); err != nil {
		t.Fatalf("matching tag from POST race was rejected: %v: %s", err, output)
	}

	env["MOCK_GH_STATE"] = filepath.Join(mockDir, "mismatched-raced-tag.sha")
	env["RELEASE_SHA"] = strings.Repeat("d", 40)
	env["MOCK_GH_SHA"] = strings.Repeat("e", 40)
	if _, err := runReleaseScript(t, "scripts/release-tag.sh", []string{"ensure"}, env); err == nil {
		t.Fatal("POST race accepted a tag with a different SHA")
	}
}

func TestReleaseNotesScriptRequiresOneMatchingPullRequest(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("release workflow runs on Linux")
	}
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
			commandOutput, err := runReleaseScript(t, "scripts/release-notes.sh", []string{input, output}, map[string]string{
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
				t.Fatalf("release notes: %v: %s", err, commandOutput)
			}
			notes := string(readTestFile(t, output))
			for _, want := range []string{"Pull request #7", "Release $(false)", "_No description provided._", "@author", "https://example.test/pr/7"} {
				if !strings.Contains(notes, want) {
					t.Errorf("release notes do not contain %q: %s", want, notes)
				}
			}
		})
	}
}

func runReleaseScript(t *testing.T, script string, args []string, extraEnv map[string]string) ([]byte, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	commandArgs := append([]string{script}, args...)
	// Tests pass only repository-owned scripts selected by the caller.
	//nolint:gosec
	command := exec.CommandContext(ctx, "bash", commandArgs...)
	command.Env = replaceTestEnvironment(os.Environ(), extraEnv)
	output, err := command.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("%s timed out: %v", script, ctx.Err())
	}
	return output, err
}

func replaceTestEnvironment(environment []string, replacements map[string]string) []string {
	for key, value := range replacements {
		entry := key + "=" + value
		prefix := key + "="
		replaced := false
		for index, existing := range environment {
			if strings.HasPrefix(existing, prefix) {
				environment[index] = entry
				replaced = true
				break
			}
		}
		if !replaced {
			environment = append(environment, entry)
		}
	}
	return environment
}
