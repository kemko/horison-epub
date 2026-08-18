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
		if !documentsMakeTarget(readme, target) {
			t.Errorf("README.md does not mention Makefile target %q", target)
		}
		if !documentsMakeTarget(claude, target) {
			t.Errorf("CLAUDE.md does not mention Makefile target %q", target)
		}
	}
}

func documentsMakeTarget(document, target string) bool {
	for line := range strings.SplitSeq(document, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "make" && fields[1] == target {
			return true
		}
	}
	return false
}
