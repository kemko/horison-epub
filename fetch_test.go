package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newTestFetcher(t *testing.T) *Fetcher {
	t.Helper()
	fetcher, err := NewFetcher()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := fetcher.Close(); err != nil {
			t.Errorf("close fetcher: %v", err)
		}
	})
	return fetcher
}

func TestFetchHTMLSuccessRedirectAndRelativeURLs(t *testing.T) {
	var userAgent string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userAgent = r.UserAgent()
		switch r.URL.Path {
		case "/start":
			http.Redirect(w, r, "/issues/issue/", http.StatusFound)
		case "/issues/issue/":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, `<h1 class="entry-title">Выпуск</h1><div class="entry-content"><img src="../cover.jpg"><h2>Содержание</h2><h4>Раздел</h4><p><a href="article/">Статья</a></p></div>`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	fetcher := newTestFetcher(t)
	body, finalURL, err := fetcher.FetchHTML(server.URL + "/start")
	if err != nil {
		t.Fatal(err)
	}
	if finalURL != server.URL+"/issues/issue/" {
		t.Fatalf("final URL = %q, want %q", finalURL, server.URL+"/issues/issue/")
	}
	if userAgent != defaultUserAgent {
		t.Fatalf("User-Agent = %q, want %q", userAgent, defaultUserAgent)
	}
	issue, err := ParseIssue(bytes.NewReader(body), finalURL)
	if err != nil {
		t.Fatal(err)
	}
	if issue.CoverURL != server.URL+"/issues/cover.jpg" {
		t.Fatalf("cover URL = %q", issue.CoverURL)
	}
	if got := issue.Sections[0].Articles[0].URL; got != server.URL+"/issues/issue/article/" {
		t.Fatalf("article URL = %q", got)
	}
}

func TestFetchHTMLHTTPErrorIncludesURL(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()

	fetcher := newTestFetcher(t)
	_, _, err := fetcher.FetchHTML(server.URL + "/missing")
	if err == nil || !strings.Contains(err.Error(), server.URL+"/missing") || !strings.Contains(err.Error(), "404") {
		t.Fatalf("error = %v", err)
	}
}

func TestFetchHTMLRejectsInvalidSchemeAndOversize(t *testing.T) {
	fetcher := newTestFetcher(t)
	if _, _, err := fetcher.FetchHTML("ftp://example.test/file"); err == nil || !strings.Contains(err.Error(), "ftp://example.test/file") {
		t.Fatalf("invalid scheme error = %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write(bytes.Repeat([]byte("x"), maxHTMLBytes+1))
	}))
	defer server.Close()
	_, _, err := fetcher.FetchHTML(server.URL)
	if err == nil || !strings.Contains(err.Error(), "16") || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("oversize error = %v", err)
	}
}

func TestFetchHTMLTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		_, _ = w.Write([]byte("late"))
	}))
	defer server.Close()

	fetcher := newTestFetcher(t)
	fetcher.Client = &http.Client{Timeout: 10 * time.Millisecond}
	_, _, err := fetcher.FetchHTML(server.URL)
	if err == nil || !strings.Contains(err.Error(), server.URL) || !strings.Contains(strings.ToLower(err.Error()), "timeout") {
		t.Fatalf("timeout error = %v", err)
	}
}

func TestFetchImageValidatesMIMEAndExtension(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/wrong-mime.jpg":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(jpegBytes())
		case "/wrong-extension.txt":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(pngBytes())
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	fetcher := newTestFetcher(t)
	for _, rawURL := range []string{server.URL + "/wrong-mime.jpg", server.URL + "/wrong-extension.txt"} {
		if _, err := fetcher.FetchImage(rawURL); err == nil || !strings.Contains(err.Error(), rawURL) {
			t.Errorf("FetchImage(%q) error = %v", rawURL, err)
		}
	}
}

func TestFetchImageRejectsOversize(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(append(pngBytes(), bytes.Repeat([]byte("x"), maxImageBytes)...))
	}))
	defer server.Close()

	fetcher := newTestFetcher(t)
	_, err := fetcher.FetchImage(server.URL + "/large.png")
	if err == nil || !strings.Contains(err.Error(), "32") || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("oversize error = %v", err)
	}
}

func TestFetchImageRedirectDeduplicatesAndStoresFile(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "/assets/photo.png", http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes())
	}))
	defer server.Close()

	fetcher := newTestFetcher(t)
	first, err := fetcher.FetchImage(server.URL + "/redirect")
	if err != nil {
		t.Fatal(err)
	}
	second, err := fetcher.FetchImage(server.URL + "/redirect")
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("deduplicated image differs: first=%+v second=%+v", first, second)
	}
	if first.URL != server.URL+"/assets/photo.png" || first.MIME != "image/png" {
		t.Fatalf("image = %+v", first)
	}
	if filepath.Ext(first.Path) != ".png" {
		t.Fatalf("image path = %q", first.Path)
	}
	data, err := os.ReadFile(first.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, pngBytes()) {
		t.Fatalf("stored image differs from response")
	}
	if requests.Load() != 2 {
		t.Fatalf("request count = %d, want redirect plus one image request", requests.Load())
	}
}

func TestFetchImageSupportsSVGWithoutExtension(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/svg+xml")
		_, _ = w.Write([]byte(`<svg xmlns="http://www.w3.org/2000/svg"><path/></svg>`))
	}))
	defer server.Close()

	fetcher := newTestFetcher(t)
	image, err := fetcher.FetchImage(server.URL + "/image")
	if err != nil {
		t.Fatal(err)
	}
	if image.MIME != "image/svg+xml" || filepath.Ext(image.Path) != ".svg" {
		t.Fatalf("image = %+v", image)
	}
}

func jpegBytes() []byte {
	return []byte{0xff, 0xd8, 0xff, 0xd9}
}

func pngBytes() []byte {
	return []byte("\x89PNG\r\n\x1a\nimage")
}
