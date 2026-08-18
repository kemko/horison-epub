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

	for _, issueURL := range []string{"https://example.test/", "https://example.test/issues/%2F/"} {
		if _, err := outputPathFor(issueURL, ""); err == nil {
			t.Errorf("outputPathFor(%q) accepted unsafe default", issueURL)
		}
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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	content, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "keep" {
		t.Fatalf("existing output was changed to %q", content)
	}
}

func TestWriteEPUBDoesNotReplaceOutputCreatedBeforePublish(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	content, readErr := os.ReadFile(output)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(content) != "keep" {
		t.Fatalf("existing output was changed to %q", content)
	}
}

func TestRunBuildsEPUBAndPrintsOutput(t *testing.T) {
	server := newCLITestServer(t, false)
	defer server.Close()

	output := filepath.Join(t.TempDir(), "book.epub")
	var stdout, stderr bytes.Buffer
	if err := run([]string{"-o", output, server.URL + "/issues/demo/"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(stdout.String()); got != output {
		t.Fatalf("stdout = %q, want %q", got, output)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q", stderr.String())
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
		if strings.Contains(file.Name, ".horizont-epub-") {
			t.Fatalf("temporary filename leaked into EPUB: %q", file.Name)
		}
	}
}

func TestRunRemovesTemporaryOutputWhenImageFails(t *testing.T) {
	server := newCLITestServer(t, true)
	defer server.Close()

	directory := t.TempDir()
	output := filepath.Join(directory, "failed.epub")
	err := run([]string{"-o", output, server.URL + "/issues/demo/"}, io.Discard, io.Discard)
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
	err := run([]string{"-o", output, server.URL + "/issues/demo/"}, io.Discard, io.Discard)
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
	err := run([]string{"-o", output, server.URL + "/issues/demo/"}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "missing title") {
		t.Fatalf("parse error = %v", err)
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
