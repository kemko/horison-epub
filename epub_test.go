package main

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	goepub "github.com/go-shiori/go-epub"
)

func TestBuildEPUBCreatesAutonomousNestedBook(t *testing.T) {
	var sharedRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cover.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(pngBytes())
		case "/shared.png":
			sharedRequests.Add(1)
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(pngBytes())
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	issueURL := server.URL + "/issues/sample/"
	issue := Issue{
		Title:    "Тестовый выпуск",
		URL:      issueURL,
		CoverURL: server.URL + "/cover.png",
		Sections: []Section{
			{
				Title: "Первый блок",
				Articles: []Article{
					{Title: "Первая статья", URL: issueURL + "first/", Author: "Автор 1", Annotation: "Аннотация 1"},
				},
			},
			{
				Title: "Второй блок",
				Articles: []Article{
					{Title: "Вторая статья", URL: issueURL + "second/", Author: "Автор 2", Annotation: "Аннотация 2"},
				},
			},
		},
	}
	sharedURL := server.URL + "/shared.png"
	articles := []Article{
		parseTestArticle(t, "Первая статья", issueURL+"first/", fmt.Sprintf(`<p>Полный текст первой статьи.</p><figure><img src="%s" srcset="ignored 2x" sizes="100vw" data-wp-image="1" alt="Иллюстрация"><figcaption>Подпись к иллюстрации</figcaption></figure><script>удалить</script>`, sharedURL)),
		parseTestArticle(t, "Вторая статья", issueURL+"second/", fmt.Sprintf(`<h2>Заголовок</h2><p>Полный текст второй статьи.</p><p><img src="%s" alt="Повтор"></p>`, sharedURL)),
	}

	fetcher := newTestFetcher(t)
	output := filepath.Join(t.TempDir(), "book.epub")
	outputFile := createTestFile(t, output)
	if err := BuildEPUB(issue, articles, fetcher, outputFile); err != nil {
		_ = outputFile.Close()
		t.Fatal(err)
	}
	if err := outputFile.Close(); err != nil {
		t.Fatal(err)
	}
	if sharedRequests.Load() != 1 {
		t.Fatalf("shared image requests = %d, want 1", sharedRequests.Load())
	}

	entries := readZipEntries(t, output)
	if got := string(entries["mimetype"].body); got != "application/epub+zip" {
		t.Fatalf("mimetype = %q", got)
	}
	if entries["mimetype"].method != zip.Store {
		t.Fatal("mimetype must be stored without compression")
	}
	for _, name := range []string{"META-INF/container.xml", "EPUB/package.opf", "EPUB/nav.xhtml", "EPUB/toc.ncx", "EPUB/css/horizont.css", "EPUB/css/cover.css", "EPUB/xhtml/cover.xhtml", "EPUB/xhtml/contents.xhtml", "EPUB/xhtml/article-001-001.xhtml", "EPUB/xhtml/article-002-001.xhtml", "EPUB/images/cover.png", "EPUB/images/image-001.png"} {
		if _, ok := entries[name]; !ok {
			t.Errorf("missing archive entry %q", name)
		}
	}
	imageCount := 0
	for name := range entries {
		if strings.HasPrefix(name, "EPUB/images/") && name != "EPUB/images/cover.png" {
			imageCount++
		}
	}
	if imageCount != 1 {
		t.Fatalf("article image count = %d, want 1", imageCount)
	}
	for _, name := range []string{"EPUB/images/cover.png", "EPUB/images/image-001.png"} {
		if _, err := png.DecodeConfig(bytes.NewReader(entries[name].body)); err != nil {
			t.Errorf("%s is not a valid PNG: %v", name, err)
		}
	}

	packageXML := string(entries["EPUB/package.opf"].body)
	for _, want := range []string{"Тестовый выпуск", "<dc:language>ru</dc:language>", issueURL, "cover-image"} {
		if !strings.Contains(packageXML, want) {
			t.Errorf("package.opf does not contain %q", want)
		}
	}
	nav := string(entries["EPUB/nav.xhtml"].body)
	for _, want := range []string{"Содержание", "Первый блок", "Второй блок", "Первая статья", "Вторая статья"} {
		if !strings.Contains(nav, want) {
			t.Errorf("nav.xhtml does not contain %q", want)
		}
	}
	assertNestedNavigation(t, entries)
	contents := string(entries["EPUB/xhtml/contents.xhtml"].body)
	for _, want := range []string{"Автор 1", "Первая статья", "Аннотация 1", "Автор 2", "Аннотация 2", "article-001-001.xhtml"} {
		if !strings.Contains(contents, want) {
			t.Errorf("contents.xhtml does not contain %q", want)
		}
	}
	article := string(entries["EPUB/xhtml/article-001-001.xhtml"].body)
	for _, want := range []string{"Первая статья", "Автор 1", "Полный текст первой статьи", "../images/image-001.png", "Подпись к иллюстрации", `alt="Иллюстрация"`} {
		if !strings.Contains(article, want) {
			t.Errorf("article XHTML does not contain %q", want)
		}
	}
	for _, unwanted := range []string{"srcset", "sizes", "data-wp-image", "<script", "удалить", sharedURL} {
		if strings.Contains(article, unwanted) {
			t.Errorf("article XHTML contains removed value %q", unwanted)
		}
	}
}

func parseTestArticle(t *testing.T, title, articleURL, body string) Article {
	t.Helper()
	article, err := ParseArticle(strings.NewReader(fmt.Sprintf(`<h1 class="entry-title">%s</h1><div class="entry-content">%s</div>`, title, body)), articleURL)
	if err != nil {
		t.Fatal(err)
	}
	return article
}

type navigationWant struct {
	title    string
	href     string
	children []navigationWant
}

type navItem struct {
	Link struct {
		Href string `xml:"href,attr"`
		Text string `xml:",chardata"`
	} `xml:"a"`
	Children []navItem `xml:"ol>li"`
}

type ncxPoint struct {
	Text    string `xml:"navLabel>text"`
	Content struct {
		Src string `xml:"src,attr"`
	} `xml:"content"`
	Children []ncxPoint `xml:"navPoint"`
}

func assertNestedNavigation(t *testing.T, entries map[string]zipEntry) {
	t.Helper()
	want := []navigationWant{
		{title: "Содержание", href: "xhtml/contents.xhtml"},
		{title: "Первый блок", href: "xhtml/section-001.xhtml", children: []navigationWant{{title: "Первая статья", href: "xhtml/article-001-001.xhtml"}}},
		{title: "Второй блок", href: "xhtml/section-002.xhtml", children: []navigationWant{{title: "Вторая статья", href: "xhtml/article-002-001.xhtml"}}},
	}

	var nav struct {
		Body struct {
			Nav struct {
				Items []navItem `xml:"ol>li"`
			} `xml:"nav"`
		} `xml:"body"`
	}
	if err := xml.Unmarshal(entries["EPUB/nav.xhtml"].body, &nav); err != nil {
		t.Fatal(err)
	}
	assertNavItems(t, entries, nav.Body.Nav.Items, want)

	var ncx struct {
		Points []ncxPoint `xml:"navMap>navPoint"`
	}
	if err := xml.Unmarshal(entries["EPUB/toc.ncx"].body, &ncx); err != nil {
		t.Fatal(err)
	}
	assertNCXPoints(t, entries, ncx.Points, want)
}

func assertNavItems(t *testing.T, entries map[string]zipEntry, got []navItem, want []navigationWant) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("nav items = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Link.Text != want[i].title || got[i].Link.Href != want[i].href {
			t.Errorf("nav item %d = %q %q, want %q %q", i, got[i].Link.Text, got[i].Link.Href, want[i].title, want[i].href)
		}
		if _, ok := entries["EPUB/"+got[i].Link.Href]; !ok {
			t.Errorf("nav href %q does not resolve", got[i].Link.Href)
		}
		assertNavItems(t, entries, got[i].Children, want[i].children)
	}
}

func assertNCXPoints(t *testing.T, entries map[string]zipEntry, got []ncxPoint, want []navigationWant) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("NCX points = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Text != want[i].title || got[i].Content.Src != want[i].href {
			t.Errorf("NCX point %d = %q %q, want %q %q", i, got[i].Text, got[i].Content.Src, want[i].title, want[i].href)
		}
		if _, ok := entries["EPUB/"+got[i].Content.Src]; !ok {
			t.Errorf("NCX src %q does not resolve", got[i].Content.Src)
		}
		assertNCXPoints(t, entries, got[i].Children, want[i].children)
	}
}

func TestBuildEPUBRequiresEveryIssueMaterial(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes())
	}))
	defer server.Close()

	fetcher := newTestFetcher(t)
	issue := Issue{
		Title:    "Выпуск",
		URL:      server.URL + "/issues/one/",
		CoverURL: server.URL + "/cover.png",
		Sections: []Section{{Title: "Раздел", Articles: []Article{{Title: "Статья", URL: server.URL + "/issues/one/article/"}}}},
	}
	err := BuildEPUB(issue, nil, fetcher, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "article was not fetched") {
		t.Fatalf("error = %v", err)
	}
}

func TestNewEPUBDoesNotUseSharedTemporaryStorage(t *testing.T) {
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))
	if err := goepub.Use(goepub.OsFS); err != nil {
		t.Fatal(err)
	}

	epubBuildMu.Lock()
	defer epubBuildMu.Unlock()
	epubFile, err := newEPUB("Выпуск")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := epubFile.AddSection("<p>Текст</p>", "Раздел", "section.xhtml", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := epubFile.WriteTo(io.Discard); err != nil {
		t.Fatalf("write with unavailable OS temp storage: %v", err)
	}
}

func TestBuildEPUBRejectsMissingDependenciesAndMetadata(t *testing.T) {
	validIssue := Issue{
		Title:    "Выпуск",
		URL:      "https://example.test/issues/one/",
		CoverURL: "https://example.test/cover.png",
	}
	fetcher := newTestFetcher(t)
	credentialURL := fmt.Sprintf("https://%s:%s@example.test/issues/one/", "user", "secret")
	tests := []struct {
		name    string
		issue   Issue
		fetcher *Fetcher
		output  io.Writer
		want    string
	}{
		{name: "fetcher", issue: validIssue, output: io.Discard, want: "missing fetcher"},
		{name: "output", issue: validIssue, fetcher: fetcher, want: "missing output"},
		{name: "title", issue: Issue{URL: validIssue.URL, CoverURL: validIssue.CoverURL}, fetcher: fetcher, output: io.Discard, want: "incomplete issue metadata"},
		{name: "URL", issue: Issue{Title: validIssue.Title, CoverURL: validIssue.CoverURL}, fetcher: fetcher, output: io.Discard, want: "incomplete issue metadata"},
		{name: "cover", issue: Issue{Title: validIssue.Title, URL: validIssue.URL}, fetcher: fetcher, output: io.Discard, want: "incomplete issue metadata"},
		{name: "credentials", issue: Issue{Title: validIssue.Title, URL: credentialURL, CoverURL: validIssue.CoverURL}, fetcher: fetcher, output: io.Discard, want: "userinfo is not allowed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := BuildEPUB(test.issue, nil, test.fetcher, test.output)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestValidateEPUBArchiveRejectsMissingSection(t *testing.T) {
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	header := &zip.FileHeader{Name: "mimetype", Method: zip.Store}
	mimetype, err := writer.CreateHeader(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mimetype.Write([]byte("application/epub+zip")); err != nil {
		t.Fatal(err)
	}
	packageFile, err := writer.Create("EPUB/package.opf")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := packageFile.Write([]byte(`<package><manifest><item href="xhtml/article-001-001.xhtml"></item></manifest></package>`)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	archive, err := zip.NewReader(bytes.NewReader(buffer.Bytes()), int64(buffer.Len()))
	if err != nil {
		t.Fatal(err)
	}
	err = validateEPUBArchive(archive, map[string]struct{}{
		"mimetype":                         {},
		"EPUB/package.opf":                 {},
		"EPUB/xhtml/article-001-001.xhtml": {},
	})
	if err == nil || !strings.Contains(err.Error(), "article-001-001.xhtml") {
		t.Fatalf("error = %v", err)
	}
}

type zipEntry struct {
	body   []byte
	method uint16
}

func readZipEntries(t *testing.T, path string) map[string]zipEntry {
	t.Helper()
	archive, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := archive.Close(); err != nil {
			t.Errorf("close EPUB: %v", err)
		}
	}()

	entries := make(map[string]zipEntry, len(archive.File))
	for _, file := range archive.File {
		reader, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(reader)
		closeErr := reader.Close()
		if err != nil {
			t.Fatal(err)
		}
		if closeErr != nil {
			t.Fatal(closeErr)
		}
		entries[file.Name] = zipEntry{body: body, method: file.Method}
	}
	return entries
}
