package main

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
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
			_, _ = fmt.Fprint(w, `<h1 class="entry-title">Выпуск</h1><div class="entry-content"><img src="../cover.jpg"><h2>Содержание</h2><h4>Раздел</h4><p><a href="article/">Статья</a></p></div>`)
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
	fetcher.client = &http.Client{Timeout: 10 * time.Millisecond}
	_, _, err := fetcher.FetchHTML(server.URL)
	if err == nil || !strings.Contains(err.Error(), server.URL) || !strings.Contains(strings.ToLower(err.Error()), "timeout") {
		t.Fatalf("timeout error = %v", err)
	}
}

func TestFetchHTMLStopsAfterTenRedirects(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Redirect(w, r, "/loop", http.StatusFound)
	}))
	defer server.Close()

	fetcher := newTestFetcher(t)
	_, _, err := fetcher.FetchHTML(server.URL + "/loop")
	if err == nil || !strings.Contains(err.Error(), "10 redirects") {
		t.Fatalf("redirect error = %v", err)
	}
	if requests.Load() > 10 {
		t.Fatalf("requests = %d, want at most 10", requests.Load())
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

func TestFetchImageSupportsValidFormats(t *testing.T) {
	tests := []struct {
		name string
		path string
		mime string
		body []byte
		ext  string
	}{
		{name: "JPEG", path: "/image.jpeg", mime: "image/jpeg", body: jpegBytes(), ext: ".jpg"},
		{name: "PNG", path: "/image.png", mime: "image/png", body: pngBytes(), ext: ".png"},
		{name: "GIF", path: "/image.gif", mime: "image/gif", body: gifBytes(), ext: ".gif"},
		{
			name: "SVG without extension",
			path: "/image",
			mime: "image/svg+xml",
			body: []byte(`<svg xmlns="http://www.w3.org/2000/svg"><defs><linearGradient id="paint"/></defs><path fill="url(#paint)"/><use href="#shape"/></svg>`),
			ext:  ".svg",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tt.mime)
				_, _ = w.Write(tt.body)
			}))
			defer server.Close()

			fetched, err := newTestFetcher(t).FetchImage(server.URL + tt.path)
			if err != nil {
				t.Fatal(err)
			}
			if fetched.MIME != tt.mime || filepath.Ext(fetched.Path) != tt.ext {
				t.Fatalf("image = %+v", fetched)
			}
		})
	}
}

func TestFetchImageRejectsCorruptRasterImages(t *testing.T) {
	tests := []struct {
		path string
		mime string
		body []byte
	}{
		{path: "/bad.jpg", mime: "image/jpeg", body: []byte{0xff, 0xd8, 0xff, 0xd9}},
		{path: "/bad.png", mime: "image/png", body: []byte("\x89PNG\r\n\x1a\n")},
		{path: "/bad.gif", mime: "image/gif", body: []byte("GIF89a")},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tt.mime)
				_, _ = w.Write(tt.body)
			}))
			defer server.Close()

			if _, err := newTestFetcher(t).FetchImage(server.URL + tt.path); err == nil {
				t.Fatal("corrupt image was accepted")
			}
		})
	}
}

func TestFetchImageRejectsUnsafeSVG(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "script", body: `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`},
		{name: "event handler", body: `<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"/>`},
		{name: "external image", body: `<svg xmlns="http://www.w3.org/2000/svg"><image href="https://example.test/image.png"/></svg>`},
		{name: "external CSS URL", body: `<svg xmlns="http://www.w3.org/2000/svg"><path fill="url(https://example.test/paint.svg)"/></svg>`},
		{name: "stylesheet", body: `<?xml-stylesheet href="https://example.test/style.css"?><svg xmlns="http://www.w3.org/2000/svg"/>`},
		{name: "foreign object", body: `<svg xmlns="http://www.w3.org/2000/svg"><foreignObject/></svg>`},
		{name: "animation", body: `<svg xmlns="http://www.w3.org/2000/svg"><animateColor/></svg>`},
		{name: "malformed", body: `<svg xmlns="http://www.w3.org/2000/svg"><path></svg>`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "image/svg+xml")
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			if _, err := newTestFetcher(t).FetchImage(server.URL + "/image.svg"); err == nil || !strings.Contains(err.Error(), "SVG") {
				t.Fatalf("unsafe SVG error = %v", err)
			}
		})
	}
}

func jpegBytes() []byte {
	var buffer bytes.Buffer
	if err := jpeg.Encode(&buffer, testImage(), nil); err != nil {
		panic(err)
	}
	return buffer.Bytes()
}

func pngBytes() []byte {
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, testImage()); err != nil {
		panic(err)
	}
	return buffer.Bytes()
}

func gifBytes() []byte {
	var buffer bytes.Buffer
	if err := gif.Encode(&buffer, testImage(), nil); err != nil {
		panic(err)
	}
	return buffer.Bytes()
}

func testImage() image.Image {
	value := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	value.Set(0, 0, color.NRGBA{R: 0x20, G: 0x40, B: 0x60, A: 0xff})
	return value
}
