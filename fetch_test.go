package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"net"
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
	fetcher, err := newFetcher(true)
	if err != nil {
		t.Fatal(err)
	}
	tempDir := fetcher.tempDir
	t.Cleanup(func() {
		if err := fetcher.Close(); err != nil {
			t.Errorf("close fetcher: %v", err)
		}
		if _, err := os.Stat(tempDir); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("temporary directory still exists: %v", err)
		}
	})
	return fetcher
}

func TestFetcherRejectsPrivateNetworks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("request reached private HTTP server")
	}))
	defer server.Close()

	fetcher, err := NewFetcher()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := fetcher.Close(); err != nil {
			t.Errorf("close fetcher: %v", err)
		}
	})
	_, _, err = fetcher.FetchHTML(server.URL)
	if err == nil || !strings.Contains(err.Error(), "non-public destination") {
		t.Fatalf("private network error = %v", err)
	}
}

func TestPublicIPRejectsSpecialUseNetworks(t *testing.T) {
	tests := []struct {
		address string
		public  bool
	}{
		{address: "8.8.8.8", public: true},
		{address: "2606:4700:4700::1111", public: true},
		{address: "10.0.0.1"},
		{address: "100.64.0.1"},
		{address: "192.0.2.1"},
		{address: "198.18.0.1"},
		{address: "203.0.113.1"},
		{address: "2001:db8::1"},
		{address: "64:ff9b:1::1"},
		{address: "::ffff:127.0.0.1"},
	}
	for _, test := range tests {
		t.Run(test.address, func(t *testing.T) {
			if got := isPublicIP(net.ParseIP(test.address)); got != test.public {
				t.Fatalf("isPublicIP(%q) = %t, want %t", test.address, got, test.public)
			}
		})
	}
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

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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
	if first.URL != server.URL+"/assets/photo.png" {
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
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", tt.mime)
				_, _ = w.Write(tt.body)
			}))
			defer server.Close()

			fetched, err := newTestFetcher(t).FetchImage(server.URL + tt.path)
			if err != nil {
				t.Fatal(err)
			}
			if filepath.Ext(fetched.Path) != tt.ext {
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
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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

func TestFetchImageRejectsTruncatedRasterImages(t *testing.T) {
	tests := []struct {
		name         string
		body         []byte
		decodeConfig func(io.Reader) (image.Config, error)
	}{
		{name: "JPEG", body: jpegBytes(), decodeConfig: jpeg.DecodeConfig},
		{name: "PNG", body: pngBytes(), decodeConfig: png.DecodeConfig},
		{name: "GIF", body: gifBytes(), decodeConfig: gif.DecodeConfig},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := tt.body[:len(tt.body)-1]
			if _, err := tt.decodeConfig(bytes.NewReader(body)); err != nil {
				t.Fatalf("test image metadata is invalid: %v", err)
			}
			if _, err := detectImageKind(body); err == nil {
				t.Fatal("truncated image was accepted")
			}
		})
	}
}

func TestRasterImagePixelLimit(t *testing.T) {
	if err := validateRasterDimensions(image.Config{Width: maxImagePixels + 1, Height: 1}); err == nil || !strings.Contains(err.Error(), "pixel limit") {
		t.Fatalf("pixel limit error = %v", err)
	}
}

func TestGIFFrameLimits(t *testing.T) {
	if _, err := detectImageKind(animatedGIFBytes(2)); err != nil {
		t.Fatalf("valid animated GIF: %v", err)
	}
	if err := validateGIFFrames(testGIFStructure(maxGIFFrames+1, 1, 1)); err == nil || !strings.Contains(err.Error(), "frame limit") {
		t.Fatalf("frame limit error = %v", err)
	}
	if err := validateGIFFrames(testGIFStructure(2, 5_000, 5_000)); err == nil || !strings.Contains(err.Error(), "pixel limit") {
		t.Fatalf("pixel limit error = %v", err)
	}
}

func TestFetchImageRejectsUnsafeSVG(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "script", body: `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`},
		{name: "SVG Tiny handler", body: `<svg xmlns="http://www.w3.org/2000/svg" xmlns:ev="http://www.w3.org/2001/xml-events"><rect><handler type="application/ecmascript" ev:event="click">alert(1)</handler></rect></svg>`},
		{name: "event handler", body: `<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"/>`},
		{name: "external image", body: `<svg xmlns="http://www.w3.org/2000/svg"><image href="https://example.test/image.png"/></svg>`},
		{name: "external CSS URL", body: `<svg xmlns="http://www.w3.org/2000/svg"><path fill="url(https://example.test/paint.svg)"/></svg>`},
		{name: "escaped external CSS URL", body: `<svg xmlns="http://www.w3.org/2000/svg"><path fill="u\72l(https://example.test/paint.svg)"/></svg>`},
		{name: "stylesheet", body: `<?xml-stylesheet href="https://example.test/style.css"?><svg xmlns="http://www.w3.org/2000/svg"/>`},
		{name: "foreign object", body: `<svg xmlns="http://www.w3.org/2000/svg"><foreignObject/></svg>`},
		{name: "style element", body: `<svg xmlns="http://www.w3.org/2000/svg"><style>path { fill: red }</style></svg>`},
		{name: "style attribute", body: `<svg xmlns="http://www.w3.org/2000/svg"><path style="fill: red"/></svg>`},
		{name: "XML base", body: `<svg xmlns="http://www.w3.org/2000/svg" xml:base="https://example.test/"/>`},
		{name: "foreign namespace", body: `<svg xmlns="http://www.w3.org/2000/svg" xmlns:x="https://example.test/x"><x:item/></svg>`},
		{name: "external src", body: `<svg xmlns="http://www.w3.org/2000/svg"><image src="https://example.test/image.png"/></svg>`},
		{name: "animation", body: `<svg xmlns="http://www.w3.org/2000/svg"><animateColor/></svg>`},
		{name: "malformed", body: `<svg xmlns="http://www.w3.org/2000/svg"><path></svg>`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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

func TestFetcherEnforcesIssueBudgets(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte("x"))
	}))
	defer server.Close()

	fetcher := newTestFetcher(t)
	fetcher.requests = maxFetchRequests
	if _, _, err := fetcher.FetchHTML(server.URL); err == nil || !strings.Contains(err.Error(), "request limit") {
		t.Fatalf("request limit error = %v", err)
	}
	if requests.Load() != 0 {
		t.Fatalf("requests after exhausted request budget = %d", requests.Load())
	}

	fetcher.requests = 0
	fetcher.fetchedBytes = maxFetchedBytes
	if _, _, err := fetcher.FetchHTML(server.URL); err == nil || !strings.Contains(err.Error(), "download limit") {
		t.Fatalf("download limit error = %v", err)
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

func animatedGIFBytes(frames int) []byte {
	palette := color.Palette{color.Black, color.White}
	images := make([]*image.Paletted, frames)
	for index := range images {
		images[index] = image.NewPaletted(image.Rect(0, 0, 1, 1), palette)
	}
	var buffer bytes.Buffer
	if err := gif.EncodeAll(&buffer, &gif.GIF{Image: images, Delay: make([]int, frames)}); err != nil {
		panic(err)
	}
	return buffer.Bytes()
}

func testGIFStructure(frames int, width, height uint16) []byte {
	body := []byte("GIF89a\x01\x00\x01\x00\x00\x00\x00")
	dimensions := make([]byte, 4)
	binary.LittleEndian.PutUint16(dimensions[0:2], width)
	binary.LittleEndian.PutUint16(dimensions[2:4], height)
	for range frames {
		body = append(body,
			0x2c,
			0x00, 0x00, 0x00, 0x00,
		)
		body = append(body, dimensions...)
		body = append(body, 0x00, 0x02, 0x00)
	}
	return append(body, 0x3b)
}

func testImage() image.Image {
	value := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	value.Set(0, 0, color.NRGBA{R: 0x20, G: 0x40, B: 0x60, A: 0xff})
	return value
}
