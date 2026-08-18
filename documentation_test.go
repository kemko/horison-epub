package main

import (
	"os"
	"strings"
	"testing"
)

func TestDocumentationListsMakeTargets(t *testing.T) {
	makefileData, err := os.ReadFile("Makefile")
	if err != nil {
		t.Fatal(err)
	}
	readmeData, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatal(err)
	}
	claudeData, err := os.ReadFile("CLAUDE.md")
	if err != nil {
		t.Fatal(err)
	}

	makefile := string(makefileData)
	readme := string(readmeData)
	claude := string(claudeData)

	for _, target := range []string{
		"build",
		"test",
		"test-race",
		"lint",
		"coverage",
		"vuln",
		"release-check",
		"ci",
		"clean",
	} {
		if !strings.Contains(makefile, "\n"+target+":") {
			t.Errorf("Makefile does not define documented target %q", target)
		}
		if !strings.Contains(readme, "make "+target) && !strings.Contains(claude, "make "+target) {
			t.Errorf("documentation does not mention Makefile target %q", target)
		}
	}
}
