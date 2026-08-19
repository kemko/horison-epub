package main

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOutputPathFor(t *testing.T) {
	got, err := outputPathFor("https://example.test/issues/horisont-n-82/?page=1", "")
	if err != nil {
		t.Fatal(err)
	}
	if got != "horisont-n-82.epub" {
		t.Fatalf("default output = %q", got)
	}

	got, err = outputPathFor("https://example.test/issues/issue/", "custom/book.epub")
	if err != nil {
		t.Fatal(err)
	}
	if got != "custom/book.epub" {
		t.Fatalf("explicit output = %q", got)
	}

	for _, issueURL := range []string{"https://example.test/", "https://example.test/issues/%2F/", "https://example.test/issues/book%0Aname/"} {
		if _, err := outputPathFor(issueURL, ""); err == nil {
			t.Errorf("outputPathFor(%q) accepted unsafe default", issueURL)
		}
	}
	for _, requested := range []string{"book\nname.epub", "book\rname.epub"} {
		if _, err := outputPathFor("https://example.test/issues/book/", requested); err == nil {
			t.Errorf("outputPathFor accepted unsafe explicit path %q", requested)
		}
	}
}

func TestUniqueArticleURLsDeduplicatesAndLimitsMaterials(t *testing.T) {
	issue := Issue{Sections: []Section{
		{Articles: []Article{
			{Title: "One", URL: "https://example.test/issues/one/"},
			{Title: "One again", URL: "https://example.test/issues/one/#fragment"},
		}},
		{Articles: []Article{{Title: "Two", URL: "https://example.test/issues/two/"}}},
	}}
	urls, err := uniqueArticleURLs(issue)
	if err != nil {
		t.Fatal(err)
	}
	if len(urls) != 2 || urls[0] != issue.Sections[0].Articles[0].URL || urls[1] != issue.Sections[1].Articles[0].URL {
		t.Fatalf("unique URLs = %q", urls)
	}

	articles := make([]Article, maxArticlesPerIssue+1)
	for index := range articles {
		articles[index] = Article{Title: fmt.Sprint(index), URL: fmt.Sprintf("https://example.test/issues/%d/", index)}
	}
	_, err = uniqueArticleURLs(Issue{Sections: []Section{{Articles: articles}}})
	if err == nil || !strings.Contains(err.Error(), "article limit") {
		t.Fatalf("article limit error = %v", err)
	}
}

func TestDirectoryModeUnsafeIsPlatformSpecific(t *testing.T) {
	if directoryModeUnsafe("windows", 0o777) {
		t.Fatal("Windows synthetic mode bits were treated as POSIX permissions")
	}
	if directoryModeUnsafe("linux", 0o755) {
		t.Fatal("private POSIX directory was rejected")
	}
	if !directoryModeUnsafe("linux", 0o775) {
		t.Fatal("group-writable POSIX directory was accepted")
	}
}

func TestRunValidatesArgumentsAndExistingOutput(t *testing.T) {
	for _, args := range [][]string{{}, {"one", "two"}, {"-o"}} {
		if err := run(args, io.Discard, io.Discard); err == nil {
			t.Errorf("run(%q) succeeded", args)
		}
	}
	var help bytes.Buffer
	if err := run([]string{"-h"}, io.Discard, &help); err != nil || !strings.Contains(help.String(), "usage:") {
		t.Fatalf("help = %q, error = %v", help.String(), err)
	}

	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		http.Error(w, "unexpected request", http.StatusInternalServerError)
	}))
	defer server.Close()

	output := filepath.Join(t.TempDir(), "existing.epub")
	if err := os.WriteFile(output, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := run([]string{"-o", output, server.URL + "/issues/demo/"}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("existing output error = %v", err)
	}
	if requests != 0 {
		t.Fatalf("requests = %d, want 0", requests)
	}
	content := readTestFile(t, output)
	if string(content) != "keep" {
		t.Fatalf("existing output was changed to %q", content)
	}
}

func TestWriteEPUBDoesNotReplaceOutputCreatedBeforePublish(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes())
	}))
	defer server.Close()

	issueURL := server.URL + "/issues/demo/"
	issue := Issue{
		Title:    "Выпуск",
		URL:      issueURL,
		CoverURL: server.URL + "/cover.png",
		Sections: []Section{{
			Title: "Раздел",
			Articles: []Article{{
				Title: "Статья",
				URL:   issueURL + "article/",
			}},
		}},
	}
	articles := []Article{{Title: "Статья", URL: issueURL + "article/", HTML: "<p>Текст.</p>"}}
	output := filepath.Join(t.TempDir(), "existing.epub")
	if err := os.WriteFile(output, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := writeEPUB(issue, articles, newTestFetcher(t), output, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("publication error = %v", err)
	}
	content := readTestFile(t, output)
	if string(content) != "keep" {
		t.Fatalf("existing output was changed to %q", content)
	}
}

func TestWriteEPUBRejectsOutputDirectoryWritableByOthers(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "shared")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	directoryRoot, err := os.OpenRoot(filepath.Dir(directory))
	if err != nil {
		t.Fatal(err)
	}
	directoryFile, err := directoryRoot.OpenFile(filepath.Base(directory), os.O_RDONLY, 0)
	if err != nil {
		_ = directoryRoot.Close()
		t.Fatal(err)
	}
	if err := directoryFile.Chmod(0o777); err != nil {
		_ = directoryFile.Close()
		_ = directoryRoot.Close()
		t.Fatal(err)
	}
	if err := directoryFile.Close(); err != nil {
		_ = directoryRoot.Close()
		t.Fatal(err)
	}
	if err := directoryRoot.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(directory)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o022 == 0 {
		t.Skip("platform does not expose writable group/other mode bits")
	}

	err = writeEPUB(Issue{}, nil, nil, filepath.Join(directory, "book.epub"), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "writable by other users") {
		t.Fatalf("directory permission error = %v", err)
	}
	entries, readErr := os.ReadDir(directory)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("output directory contains %v", entries)
	}
}

func createTestFile(t *testing.T, path string) *os.File {
	t.Helper()
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := root.Close(); err != nil {
			t.Errorf("close test file root: %v", err)
		}
	})
	file, err := root.OpenFile(filepath.Base(path), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	return file
}

func readTestFile(t *testing.T, path string) []byte {
	t.Helper()
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	file, err := root.OpenFile(filepath.Base(path), os.O_RDONLY, 0)
	if err != nil {
		_ = root.Close()
		t.Fatal(err)
	}
	content, readErr := io.ReadAll(file)
	closeErr := file.Close()
	rootErr := root.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	if rootErr != nil {
		t.Fatal(rootErr)
	}
	return content
}

func TestRunBuildsEPUBAndPrintsOutput(t *testing.T) {
	server := newCLITestServer(t, false)
	defer server.Close()

	output := filepath.Join(t.TempDir(), "book.epub")
	var stdout bytes.Buffer
	if err := run([]string{"-allow-private-network", "-o", output, server.URL + "/issues/demo/"}, &stdout, io.Discard); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(stdout.String()); got != output {
		t.Fatalf("stdout = %q, want %q", got, output)
	}
	if _, err := os.Stat(output); err != nil {
		t.Fatal(err)
	}

	archive, err := zip.OpenReader(output)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := archive.Close(); err != nil {
			t.Errorf("close EPUB: %v", err)
		}
	}()
	for _, file := range archive.File {
		if strings.Contains(file.Name, ".horisont-epub-") {
			t.Fatalf("temporary filename leaked into EPUB: %q", file.Name)
		}
	}
}

func TestRunRemovesTemporaryOutputWhenImageFails(t *testing.T) {
	server := newCLITestServer(t, true)
	defer server.Close()

	directory := t.TempDir()
	output := filepath.Join(directory, "failed.epub")
	err := run([]string{"-allow-private-network", "-o", output, server.URL + "/issues/demo/"}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "/missing.png") {
		t.Fatalf("image error = %v", err)
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("output stat error = %v", err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("temporary output files remain: %v", entries)
	}
}

func TestRunStopsOnUnavailableArticleWithoutOutput(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		switch r.URL.Path {
		case "/issues/demo/":
			_, _ = fmt.Fprint(w, issueHTML("/issues/demo/missing-article/"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	output := filepath.Join(t.TempDir(), "failed.epub")
	err := run([]string{"-allow-private-network", "-o", output, server.URL + "/issues/demo/"}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "/missing-article/") {
		t.Fatalf("article error = %v", err)
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("output stat error = %v", err)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want issue plus failed article", requests)
	}
}

func TestRunStopsOnIssueParseErrorWithoutOutput(t *testing.T) {
	const secret = "do-not-log-this"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/issues/demo/" {
			_, _ = fmt.Fprint(w, `<div class="entry-content"><img src="/cover.png"><h2>Содержание</h2><h4>Блок</h4><p><a href="/issues/demo/article/">Статья</a></p></div>`)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	directory := t.TempDir()
	output := filepath.Join(directory, "failed.epub")
	err := run([]string{"-allow-private-network", "-o", output, server.URL + "/issues/demo/?token=" + secret + "#" + secret}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "missing title") {
		t.Fatalf("parse error = %v", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("parse error exposes query secret: %v", err)
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("output stat error = %v", err)
	}
}

func newCLITestServer(t *testing.T, missingArticleImage bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/issues/demo/":
			_, _ = fmt.Fprint(w, issueHTML("/issues/demo/article/"))
		case "/issues/demo/article/":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			if missingArticleImage {
				_, _ = fmt.Fprint(w, `<h1 class="entry-title">Статья</h1><div class="entry-content"><p>Текст статьи.</p><img src="/missing.png" alt="Иллюстрация"></div>`)
			} else {
				_, _ = fmt.Fprint(w, `<h1 class="entry-title">Статья</h1><div class="entry-content"><p>Текст статьи.</p><img src="/article.png" alt="Иллюстрация"></div>`)
			}
		case "/cover.png", "/article.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(pngBytes())
		default:
			http.NotFound(w, r)
		}
	}))
}

func issueHTML(articlePath string) string {
	return fmt.Sprintf(`<h1 class="entry-title">Тестовый выпуск</h1><div class="entry-content"><img src="/cover.png"><h2>Содержание</h2><h4>Блок</h4><p>Автор. <a href="%s">Статья</a><br>Аннотация статьи.</p></div>`, articlePath)
}
