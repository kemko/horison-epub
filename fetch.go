package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"fmt"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

const (
	maxHTMLBytes     = 16 << 20
	maxImageBytes    = 32 << 20
	maxImagePixels   = 40_000_000
	maxGIFFrames     = 1_000
	defaultUserAgent = "horizont-epub/1.0"
)

type Fetcher struct {
	client  *http.Client
	tempDir string
	images  map[string]FetchedImage
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
		client: &http.Client{
			Timeout: 60 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 10 {
					return fmt.Errorf("stopped after 10 redirects")
				}
				if _, err := parseHTTPURL(req.URL.String()); err != nil {
					return fmt.Errorf("invalid redirect URL %q: %w", req.URL.String(), err)
				}
				return nil
			},
		},
		tempDir: dir,
		images:  make(map[string]FetchedImage),
	}, nil
}

func (f *Fetcher) Close() error {
	if f == nil || f.tempDir == "" {
		return nil
	}
	return os.RemoveAll(f.tempDir)
}

func (f *Fetcher) FetchHTML(rawURL string) ([]byte, string, error) {
	response, finalURL, err := f.get(rawURL)
	if err != nil {
		return nil, "", err
	}
	body, err := readLimited(response.Body, maxHTMLBytes)
	closeErr := response.Body.Close()
	if err != nil {
		if closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close response: %w", closeErr))
		}
		return nil, "", fetchError(rawURL, "read HTML: %w", err)
	}
	if closeErr != nil {
		return nil, "", fetchError(rawURL, "close HTML response: %w", closeErr)
	}
	return body, finalURL, nil
}

func (f *Fetcher) FetchImage(rawURL string) (FetchedImage, error) {
	key, _, err := canonicalURL(rawURL)
	if err != nil {
		return FetchedImage{}, fetchError(rawURL, "invalid URL: %w", err)
	}

	if image, ok := f.images[key]; ok {
		return image, nil
	}

	response, finalURL, err := f.get(rawURL)
	if err != nil {
		return FetchedImage{}, err
	}
	body, err := readLimited(response.Body, maxImageBytes)
	closeErr := response.Body.Close()
	if err != nil {
		if closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close response: %w", closeErr))
		}
		return FetchedImage{}, fetchError(rawURL, "read image: %w", err)
	}
	if closeErr != nil {
		return FetchedImage{}, fetchError(rawURL, "close image response: %w", closeErr)
	}
	kind, err := detectImageKind(body)
	if err != nil {
		return FetchedImage{}, fetchError(rawURL, "%w", err)
	}
	if err := validateImageMIME(response.Header.Get("Content-Type"), kind.mime); err != nil {
		return FetchedImage{}, fetchError(rawURL, "%w", err)
	}
	if err := validateImageExtension(finalURL, kind); err != nil {
		return FetchedImage{}, fetchError(rawURL, "%w", err)
	}

	name := fmt.Sprintf("image-%x%s", sha256.Sum256([]byte(key)), kind.ext)
	filePath := filepath.Join(f.tempDir, name)
	if err := os.WriteFile(filePath, body, 0o600); err != nil {
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
	request, err := http.NewRequest(http.MethodGet, key, nil)
	if err != nil {
		return nil, "", fetchError(rawURL, "create request: %w", err)
	}
	request.Header.Set("User-Agent", defaultUserAgent)
	response, err := f.client.Do(request)
	if err != nil {
		return nil, "", fetchError(rawURL, "request: %w", err)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		statusErr := fmt.Errorf("unexpected HTTP status %s", response.Status)
		if closeErr := response.Body.Close(); closeErr != nil {
			statusErr = errors.Join(statusErr, fmt.Errorf("close response: %w", closeErr))
		}
		return nil, "", fetchError(rawURL, "%w", statusErr)
	}

	final := parsed
	if response.Request != nil && response.Request.URL != nil {
		final = response.Request.URL
	}
	finalURL, _, err := canonicalURL(final.String())
	if err != nil {
		if closeErr := response.Body.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close response: %w", closeErr))
		}
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

func detectImageKind(body []byte) (imageKind, error) {
	var kind imageKind
	switch {
	case len(body) >= 3 && body[0] == 0xff && body[1] == 0xd8 && body[2] == 0xff:
		kind = imageKind{mime: "image/jpeg", ext: ".jpg"}
	case bytes.HasPrefix(body, []byte("\x89PNG\r\n\x1a\n")):
		kind = imageKind{mime: "image/png", ext: ".png"}
	case bytes.HasPrefix(body, []byte("GIF87a")) || bytes.HasPrefix(body, []byte("GIF89a")):
		kind = imageKind{mime: "image/gif", ext: ".gif"}
	default:
		isSVG, svgErr := validateSVG(body)
		if !isSVG {
			return imageKind{}, fmt.Errorf("unsupported image MIME")
		}
		if svgErr != nil {
			return imageKind{}, fmt.Errorf("invalid SVG: %w", svgErr)
		}
		return imageKind{mime: "image/svg+xml", ext: ".svg"}, nil
	}
	if err := validateRasterImage(body, kind); err != nil {
		return imageKind{}, fmt.Errorf("invalid %s image: %w", kind.mime, err)
	}
	return kind, nil
}

func validateRasterImage(body []byte, kind imageKind) error {
	reader := bytes.NewReader(body)
	var config image.Config
	var err error
	switch kind.mime {
	case "image/jpeg":
		config, err = jpeg.DecodeConfig(reader)
	case "image/png":
		config, err = png.DecodeConfig(reader)
	case "image/gif":
		config, err = gif.DecodeConfig(reader)
	default:
		return fmt.Errorf("unsupported raster MIME %q", kind.mime)
	}
	if err != nil {
		return err
	}
	if err := validateRasterDimensions(config); err != nil {
		return err
	}

	reader.Reset(body)
	switch kind.mime {
	case "image/jpeg":
		_, err = jpeg.Decode(reader)
	case "image/png":
		_, err = png.Decode(reader)
	case "image/gif":
		if err = validateGIFFrames(body); err != nil {
			return err
		}
		_, err = gif.DecodeAll(reader)
	}
	return err
}

func validateGIFFrames(body []byte) error {
	if len(body) < 13 {
		return io.ErrUnexpectedEOF
	}
	offset := 13
	if body[10]&0x80 != 0 {
		offset += 3 << ((body[10] & 0x07) + 1)
	}
	frames := 0
	var pixels int64
	for offset < len(body) {
		block := body[offset]
		offset++
		switch block {
		case 0x21: // Extension.
			if offset >= len(body) {
				return io.ErrUnexpectedEOF
			}
			offset++ // Extension label.
			var err error
			offset, err = skipGIFSubBlocks(body, offset)
			if err != nil {
				return err
			}
		case 0x2c: // Image descriptor.
			if len(body)-offset < 9 {
				return io.ErrUnexpectedEOF
			}
			width := int64(binary.LittleEndian.Uint16(body[offset+4 : offset+6]))
			height := int64(binary.LittleEndian.Uint16(body[offset+6 : offset+8]))
			if width == 0 || height == 0 {
				return fmt.Errorf("GIF frame has invalid dimensions %dx%d", width, height)
			}
			frames++
			if frames > maxGIFFrames {
				return fmt.Errorf("GIF exceeds %d-frame limit", maxGIFFrames)
			}
			pixels += width * height
			if pixels > maxImagePixels {
				return fmt.Errorf("GIF frames exceed %d-pixel limit", maxImagePixels)
			}
			packed := body[offset+8]
			offset += 9
			if packed&0x80 != 0 {
				offset += 3 << ((packed & 0x07) + 1)
			}
			if offset >= len(body) {
				return io.ErrUnexpectedEOF
			}
			offset++ // LZW minimum code size.
			var err error
			offset, err = skipGIFSubBlocks(body, offset)
			if err != nil {
				return err
			}
		case 0x3b: // Trailer.
			if frames == 0 {
				return fmt.Errorf("GIF contains no frames")
			}
			return nil
		default:
			return fmt.Errorf("GIF contains unknown block type 0x%02x", block)
		}
	}
	return io.ErrUnexpectedEOF
}

func skipGIFSubBlocks(body []byte, offset int) (int, error) {
	for {
		if offset >= len(body) {
			return 0, io.ErrUnexpectedEOF
		}
		size := int(body[offset])
		offset++
		if size == 0 {
			return offset, nil
		}
		if len(body)-offset < size {
			return 0, io.ErrUnexpectedEOF
		}
		offset += size
	}
}

func validateRasterDimensions(config image.Config) error {
	if config.Width <= 0 || config.Height <= 0 || config.Width > maxImagePixels/config.Height {
		return fmt.Errorf("dimensions %dx%d exceed %d-pixel limit", config.Width, config.Height, maxImagePixels)
	}
	return nil
}

func validateSVG(body []byte) (bool, error) {
	decoder := xml.NewDecoder(bytes.NewReader(body))
	var pendingErr error
	seenRoot := false
	depth := 0
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			if !seenRoot {
				return false, nil
			}
			if depth != 0 {
				return true, fmt.Errorf("unclosed root element")
			}
			return true, nil
		}
		if err != nil {
			return seenRoot, err
		}
		switch value := token.(type) {
		case xml.StartElement:
			if depth == 0 {
				if seenRoot {
					return true, fmt.Errorf("multiple root elements")
				}
				if strings.ToLower(value.Name.Local) != "svg" {
					return false, nil
				}
				seenRoot = true
				if pendingErr != nil {
					return true, pendingErr
				}
			}
			depth++
			if err := validateSVGElement(value); err != nil {
				return true, err
			}
		case xml.EndElement:
			if seenRoot {
				depth--
			}
		case xml.CharData:
			if depth == 0 && strings.TrimSpace(string(value)) != "" {
				pendingErr = fmt.Errorf("data outside root element")
				if seenRoot {
					return true, pendingErr
				}
			}
		case xml.ProcInst:
			if strings.ToLower(value.Target) != "xml" {
				pendingErr = fmt.Errorf("processing instructions are not allowed")
				if seenRoot {
					return true, pendingErr
				}
			}
		case xml.Directive:
			pendingErr = fmt.Errorf("directives are not allowed")
			if seenRoot {
				return true, pendingErr
			}
		}
	}
}

func validateSVGElement(element xml.StartElement) error {
	const svgNamespace = "http://www.w3.org/2000/svg"
	if element.Name.Space != "" && element.Name.Space != svgNamespace {
		return fmt.Errorf("element %q uses a foreign namespace", element.Name.Local)
	}

	name := strings.ToLower(element.Name.Local)
	if strings.HasPrefix(name, "animate") {
		return fmt.Errorf("element %q is not allowed", element.Name.Local)
	}
	switch name {
	case "script", "foreignobject", "iframe", "object", "embed", "audio", "video", "style", "link", "set", "discard":
		return fmt.Errorf("element %q is not allowed", element.Name.Local)
	}

	for _, attribute := range element.Attr {
		name := strings.ToLower(attribute.Name.Local)
		switch {
		case strings.HasPrefix(name, "on"):
			return fmt.Errorf("event attribute %q is not allowed", attribute.Name.Local)
		case name == "style":
			return fmt.Errorf("style attributes are not allowed")
		case name == "base" && attribute.Name.Space == "http://www.w3.org/XML/1998/namespace":
			return fmt.Errorf("xml:base is not allowed")
		case name == "href" || name == "src":
			if !strings.HasPrefix(strings.TrimSpace(attribute.Value), "#") {
				return fmt.Errorf("external reference in %q is not allowed", attribute.Name.Local)
			}
		}
		if err := validateSVGURLFunctions(attribute.Value); err != nil {
			return fmt.Errorf("attribute %q: %w", attribute.Name.Local, err)
		}
	}
	return nil
}

func validateSVGURLFunctions(value string) error {
	if strings.Contains(value, `\`) {
		return fmt.Errorf("CSS escapes are not allowed")
	}
	for lower := strings.ToLower(value); ; {
		start := strings.Index(lower, "url(")
		if start < 0 {
			return nil
		}
		value = value[start+4:]
		lower = lower[start+4:]
		end := strings.Index(lower, ")")
		if end < 0 {
			return fmt.Errorf("malformed URL reference")
		}
		reference := strings.Trim(strings.TrimSpace(value[:end]), `"'`)
		if !strings.HasPrefix(reference, "#") {
			return fmt.Errorf("external URL reference is not allowed")
		}
		value = value[end+1:]
		lower = lower[end+1:]
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
	if mediaType != actual && (actual != "image/jpeg" || mediaType != "image/jpg") {
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
