package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/xml"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	maxHTMLBytes     = 16 << 20
	maxImageBytes    = 32 << 20
	defaultUserAgent = "horizont-epub/1.0"
)

type Fetcher struct {
	Client    *http.Client
	UserAgent string
	TempDir   string

	imageMu  sync.Mutex
	images   map[string]FetchedImage
	ownedDir bool
}

type FetchedImage struct {
	URL  string
	Path string
	MIME string
}

func NewFetcher() (*Fetcher, error) {
	dir, err := os.MkdirTemp("", "horizont-epub-images-")
	if err != nil {
		return nil, fmt.Errorf("create image temporary directory: %w", err)
	}
	return &Fetcher{
		Client: &http.Client{
			Timeout: 60 * time.Second,
			CheckRedirect: func(req *http.Request, _ []*http.Request) error {
				if _, err := parseHTTPURL(req.URL.String()); err != nil {
					return fmt.Errorf("invalid redirect URL %q: %w", req.URL.String(), err)
				}
				return nil
			},
		},
		UserAgent: defaultUserAgent,
		TempDir:   dir,
		images:    make(map[string]FetchedImage),
		ownedDir:  true,
	}, nil
}

func (f *Fetcher) Close() error {
	if f == nil || !f.ownedDir || f.TempDir == "" {
		return nil
	}
	return os.RemoveAll(f.TempDir)
}

func (f *Fetcher) FetchHTML(rawURL string) ([]byte, string, error) {
	response, finalURL, err := f.get(rawURL)
	if err != nil {
		return nil, "", err
	}
	defer response.Body.Close()

	body, err := readLimited(response.Body, maxHTMLBytes)
	if err != nil {
		return nil, "", fetchError(rawURL, "read HTML: %w", err)
	}
	return body, finalURL, nil
}

func (f *Fetcher) FetchImage(rawURL string) (FetchedImage, error) {
	key, _, err := canonicalURL(rawURL)
	if err != nil {
		return FetchedImage{}, fetchError(rawURL, "invalid URL: %w", err)
	}

	f.imageMu.Lock()
	defer f.imageMu.Unlock()
	if image, ok := f.images[key]; ok {
		return image, nil
	}

	response, finalURL, err := f.get(rawURL)
	if err != nil {
		return FetchedImage{}, err
	}
	defer response.Body.Close()

	body, err := readLimited(response.Body, maxImageBytes)
	if err != nil {
		return FetchedImage{}, fetchError(rawURL, "read image: %w", err)
	}
	kind, ok := detectImageKind(body)
	if !ok {
		return FetchedImage{}, fetchError(rawURL, "unsupported image MIME")
	}
	if err := validateImageMIME(response.Header.Get("Content-Type"), kind.mime); err != nil {
		return FetchedImage{}, fetchError(rawURL, "%w", err)
	}
	if err := validateImageExtension(finalURL, kind); err != nil {
		return FetchedImage{}, fetchError(rawURL, "%w", err)
	}

	if f.TempDir == "" {
		return FetchedImage{}, fetchError(rawURL, "missing image temporary directory")
	}
	if err := os.MkdirAll(f.TempDir, 0o755); err != nil {
		return FetchedImage{}, fetchError(rawURL, "create image temporary directory: %w", err)
	}
	name := fmt.Sprintf("image-%x%s", sha256.Sum256([]byte(key)), kind.ext)
	filePath := filepath.Join(f.TempDir, name)
	temporary, err := os.CreateTemp(f.TempDir, ".image-*")
	if err != nil {
		return FetchedImage{}, fetchError(rawURL, "create temporary image: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if _, err := temporary.Write(body); err != nil {
		temporary.Close()
		return FetchedImage{}, fetchError(rawURL, "write temporary image: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return FetchedImage{}, fetchError(rawURL, "close temporary image: %w", err)
	}
	if err := os.Rename(temporaryName, filePath); err != nil {
		return FetchedImage{}, fetchError(rawURL, "store image: %w", err)
	}

	image := FetchedImage{URL: finalURL, Path: filePath, MIME: kind.mime}
	f.images[key] = image
	return image, nil
}

func (f *Fetcher) get(rawURL string) (*http.Response, string, error) {
	key, parsed, err := canonicalURL(rawURL)
	if err != nil {
		return nil, "", fetchError(rawURL, "invalid URL: %w", err)
	}
	client := f.Client
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	request, err := http.NewRequest(http.MethodGet, key, nil)
	if err != nil {
		return nil, "", fetchError(rawURL, "create request: %w", err)
	}
	userAgent := f.UserAgent
	if userAgent == "" {
		userAgent = defaultUserAgent
	}
	request.Header.Set("User-Agent", userAgent)
	response, err := client.Do(request)
	if err != nil {
		return nil, "", fetchError(rawURL, "request: %w", err)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		response.Body.Close()
		return nil, "", fetchError(rawURL, "unexpected HTTP status %s", response.Status)
	}

	final := parsed
	if response.Request != nil && response.Request.URL != nil {
		final = response.Request.URL
	}
	finalURL, _, err := canonicalURL(final.String())
	if err != nil {
		response.Body.Close()
		return nil, "", fetchError(rawURL, "invalid final URL: %w", err)
	}
	return response, finalURL, nil
}

func canonicalURL(raw string) (string, *url.URL, error) {
	u, err := parseHTTPURL(raw)
	if err != nil {
		return "", nil, err
	}
	u.Fragment = ""
	return u.String(), u, nil
}

func readLimited(reader io.Reader, limit int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("response exceeds %d-byte limit", limit)
	}
	return body, nil
}

type imageKind struct {
	mime string
	ext  string
}

func detectImageKind(body []byte) (imageKind, bool) {
	switch {
	case len(body) >= 3 && body[0] == 0xff && body[1] == 0xd8 && body[2] == 0xff:
		return imageKind{mime: "image/jpeg", ext: ".jpg"}, true
	case bytes.HasPrefix(body, []byte("\x89PNG\r\n\x1a\n")):
		return imageKind{mime: "image/png", ext: ".png"}, true
	case bytes.HasPrefix(body, []byte("GIF87a")) || bytes.HasPrefix(body, []byte("GIF89a")):
		return imageKind{mime: "image/gif", ext: ".gif"}, true
	case isSVG(body):
		return imageKind{mime: "image/svg+xml", ext: ".svg"}, true
	default:
		return imageKind{}, false
	}
}

func isSVG(body []byte) bool {
	decoder := xml.NewDecoder(bytes.NewReader(body))
	for {
		token, err := decoder.Token()
		if err != nil {
			return false
		}
		if start, ok := token.(xml.StartElement); ok {
			return start.Name.Local == "svg"
		}
	}
}

func validateImageMIME(header, actual string) error {
	if strings.TrimSpace(header) == "" {
		return nil
	}
	mediaType, _, err := mime.ParseMediaType(header)
	if err != nil {
		return fmt.Errorf("invalid image MIME %q", header)
	}
	if mediaType != actual && !(actual == "image/jpeg" && mediaType == "image/jpg") {
		return fmt.Errorf("image MIME %q does not match content %q", mediaType, actual)
	}
	return nil
}

func validateImageExtension(rawURL string, kind imageKind) error {
	u, err := parseHTTPURL(rawURL)
	if err != nil {
		return err
	}
	ext := strings.ToLower(path.Ext(u.Path))
	if ext == "" {
		return nil
	}
	supported := map[string]string{
		".jpg": ".jpg", ".jpeg": ".jpg", ".jpe": ".jpg",
		".png": ".png", ".gif": ".gif", ".svg": ".svg",
	}
	want, ok := supported[ext]
	if !ok {
		return fmt.Errorf("unsupported image extension %q", ext)
	}
	if want != kind.ext {
		return fmt.Errorf("image extension %q does not match content %q", ext, kind.mime)
	}
	return nil
}

func fetchError(rawURL, format string, args ...any) error {
	return fmt.Errorf("fetch %s: %w", rawURL, fmt.Errorf(format, args...))
}
