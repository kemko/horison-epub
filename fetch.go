package main

import (
	"bytes"
	"context"
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
	"net"
	"net/http"
	"net/netip"
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
	maxFetchRequests = 2_000
	maxFetchedBytes  = 512 << 20
	maxDecodedPixels = 512_000_000
	defaultUserAgent = "horizont-epub/1.0"
)

type Fetcher struct {
	client        *http.Client
	tempDir       string
	images        map[string]FetchedImage
	requests      int
	fetchedBytes  int64
	decodedPixels int64
}

type FetchedImage struct {
	URL  string
	Path string
}

func newFetcher(allowPrivateNetworks bool) (*Fetcher, error) {
	dir, err := os.MkdirTemp("", "horizont-epub-images-")
	if err != nil {
		return nil, fmt.Errorf("create image temporary directory: %w", err)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	if !allowPrivateNetworks {
		transport.DialContext = dialPublicNetwork
	}
	fetcher := &Fetcher{
		tempDir: dir,
		images:  make(map[string]FetchedImage),
	}
	fetcher.client = &http.Client{
		Transport: transport,
		Timeout:   60 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("stopped after 10 redirects")
			}
			if _, err := parseHTTPURL(req.URL.String()); err != nil {
				return fmt.Errorf("invalid redirect URL %q: %w", redactURL(req.URL.String()), err)
			}
			return fetcher.consumeRequest()
		},
	}
	return fetcher, nil
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
	if err := f.consumeBytes(int64(len(body))); err != nil {
		return nil, "", fetchError(rawURL, "%w", err)
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
	if err := f.consumeBytes(int64(len(body))); err != nil {
		return FetchedImage{}, fetchError(rawURL, "%w", err)
	}
	kind, err := detectImageKindWithPixelBudget(body, f.consumeDecodedPixels)
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

	image := FetchedImage{URL: finalURL, Path: filePath}
	f.images[key] = image
	return image, nil
}

func (f *Fetcher) get(rawURL string) (*http.Response, string, error) {
	key, parsed, err := canonicalURL(rawURL)
	if err != nil {
		return nil, "", fetchError(rawURL, "invalid URL: %w", err)
	}
	if err := f.consumeRequest(); err != nil {
		return nil, "", fetchError(rawURL, "%w", err)
	}
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, key, nil)
	if err != nil {
		return nil, "", fetchError(rawURL, "create request: %w", err)
	}
	request.Header.Set("User-Agent", defaultUserAgent)
	response, err := f.client.Do(request)
	if err != nil {
		return nil, "", fetchError(rawURL, "request: %w", redactURLError(err))
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

func (f *Fetcher) consumeRequest() error {
	if f.requests >= maxFetchRequests {
		return fmt.Errorf("issue exceeds %d-request limit", maxFetchRequests)
	}
	f.requests++
	return nil
}

func (f *Fetcher) consumeBytes(size int64) error {
	if size > maxFetchedBytes-f.fetchedBytes {
		return fmt.Errorf("issue exceeds %d-byte download limit", maxFetchedBytes)
	}
	f.fetchedBytes += size
	return nil
}

func (f *Fetcher) consumeDecodedPixels(size int64) error {
	if size > maxDecodedPixels-f.decodedPixels {
		return fmt.Errorf("issue exceeds %d-decoded-pixel limit", maxDecodedPixels)
	}
	f.decodedPixels += size
	return nil
}

func dialPublicNetwork(ctx context.Context, network, address string) (net.Conn, error) {
	dialer := net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	return dialPublicNetworkWith(ctx, network, address, net.DefaultResolver.LookupNetIP, dialer.DialContext)
}

func dialPublicNetworkWith(
	ctx context.Context,
	network string,
	address string,
	lookup func(context.Context, string, string) ([]netip.Addr, error),
	dial func(context.Context, string, string) (net.Conn, error),
) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	lookupNetwork := "ip"
	switch network {
	case "tcp4":
		lookupNetwork = "ip4"
	case "tcp6":
		lookupNetwork = "ip6"
	}
	addresses, err := lookup(ctx, lookupNetwork, host)
	if err != nil {
		return nil, err
	}

	publicAddresses := make([]netip.Addr, 0, len(addresses))
	for _, resolved := range addresses {
		if isPublicIP(resolved.AsSlice()) {
			publicAddresses = append(publicAddresses, resolved)
		}
	}
	if len(publicAddresses) == 0 {
		return nil, fmt.Errorf("refusing non-public destination %q", address)
	}

	var dialErr error
	for index, resolved := range publicAddresses {
		attemptContext, cancel := dialAttemptContext(ctx, len(publicAddresses)-index)
		connection, err := dial(attemptContext, network, net.JoinHostPort(resolved.String(), port))
		cancel()
		if err != nil {
			dialErr = errors.Join(dialErr, err)
			continue
		}
		remote, ok := connection.RemoteAddr().(*net.TCPAddr)
		if !ok || remote.IP == nil || !isPublicIP(remote.IP) {
			_ = connection.Close()
			continue
		}
		return connection, nil
	}
	if dialErr != nil {
		return nil, dialErr
	}
	return nil, fmt.Errorf("refusing non-public destination %q", address)
}

func dialAttemptContext(ctx context.Context, remainingAttempts int) (context.Context, context.CancelFunc) {
	deadline, ok := ctx.Deadline()
	if !ok || remainingAttempts <= 1 {
		return context.WithCancel(ctx)
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, remaining/time.Duration(remainingAttempts))
}

var specialUsePrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("3fff::/20"),
	netip.MustParsePrefix("5f00::/16"),
	netip.MustParsePrefix("fec0::/10"),
}

func isPublicIP(ip net.IP) bool {
	address, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	address = address.Unmap()
	if !address.IsGlobalUnicast() || address.IsPrivate() {
		return false
	}
	for _, prefix := range specialUsePrefixes {
		if prefix.Contains(address) {
			return false
		}
	}
	return true
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

func detectImageKindWithPixelBudget(body []byte, consumePixels func(int64) error) (imageKind, error) {
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
	if err := validateRasterImage(body, kind, consumePixels); err != nil {
		return imageKind{}, fmt.Errorf("invalid %s image: %w", kind.mime, err)
	}
	return kind, nil
}

func validateRasterImage(body []byte, kind imageKind, consumePixels func(int64) error) error {
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
	pixels := int64(config.Width) * int64(config.Height)
	if kind.mime == "image/gif" {
		pixels, err = gifFramePixels(body)
		if err != nil {
			return err
		}
	}
	if consumePixels != nil {
		if err := consumePixels(pixels); err != nil {
			return err
		}
	}

	reader.Reset(body)
	switch kind.mime {
	case "image/jpeg":
		_, err = jpeg.Decode(reader)
	case "image/png":
		_, err = png.Decode(reader)
	case "image/gif":
		_, err = gif.DecodeAll(reader)
	}
	return err
}

func gifFramePixels(body []byte) (int64, error) {
	if len(body) < 13 {
		return 0, io.ErrUnexpectedEOF
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
				return 0, io.ErrUnexpectedEOF
			}
			offset++ // Extension label.
			var err error
			offset, err = skipGIFSubBlocks(body, offset)
			if err != nil {
				return 0, err
			}
		case 0x2c: // Image descriptor.
			if len(body)-offset < 9 {
				return 0, io.ErrUnexpectedEOF
			}
			width := int64(binary.LittleEndian.Uint16(body[offset+4 : offset+6]))
			height := int64(binary.LittleEndian.Uint16(body[offset+6 : offset+8]))
			if width == 0 || height == 0 {
				return 0, fmt.Errorf("GIF frame has invalid dimensions %dx%d", width, height)
			}
			frames++
			if frames > maxGIFFrames {
				return 0, fmt.Errorf("GIF exceeds %d-frame limit", maxGIFFrames)
			}
			pixels += width * height
			if pixels > maxImagePixels {
				return 0, fmt.Errorf("GIF frames exceed %d-pixel limit", maxImagePixels)
			}
			packed := body[offset+8]
			offset += 9
			if packed&0x80 != 0 {
				offset += 3 << ((packed & 0x07) + 1)
			}
			if offset >= len(body) {
				return 0, io.ErrUnexpectedEOF
			}
			offset++ // LZW minimum code size.
			var err error
			offset, err = skipGIFSubBlocks(body, offset)
			if err != nil {
				return 0, err
			}
		case 0x3b: // Trailer.
			if frames == 0 {
				return 0, fmt.Errorf("GIF contains no frames")
			}
			return pixels, nil
		default:
			return 0, fmt.Errorf("GIF contains unknown block type 0x%02x", block)
		}
	}
	return 0, io.ErrUnexpectedEOF
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
		if errors.Is(err, io.EOF) {
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
	case "script", "handler", "listener", "foreignobject", "iframe", "object", "embed", "audio", "video", "style", "link", "set", "discard":
		return fmt.Errorf("element %q is not allowed", element.Name.Local)
	}

	for _, attribute := range element.Attr {
		switch attribute.Name.Space {
		case "", svgNamespace, "http://www.w3.org/XML/1998/namespace", "http://www.w3.org/1999/xlink", "xmlns":
		default:
			return fmt.Errorf("attribute %q uses a foreign namespace", attribute.Name.Local)
		}
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
	return fmt.Errorf("fetch %s: %w", redactURL(rawURL), fmt.Errorf(format, args...))
}

func redactURLError(err error) error {
	var requestErr *url.Error
	if !errors.As(err, &requestErr) {
		return err
	}
	redacted := *requestErr
	redacted.URL = redactURL(requestErr.URL)
	if requestErr.Err != nil && strings.HasPrefix(requestErr.Err.Error(), "failed to parse Location header ") {
		redacted.Err = redactedError{
			message: "failed to parse Location header",
			cause:   requestErr.Err,
		}
	}
	return &redacted
}

type redactedError struct {
	message string
	cause   error
}

func (e redactedError) Error() string {
	return e.message
}

func (e redactedError) Unwrap() error {
	return e.cause
}
