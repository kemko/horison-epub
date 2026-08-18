package main

import (
	"archive/zip"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
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
		{
			Title: "Первая статья",
			URL:   issueURL + "first/",
			HTML:  fmt.Sprintf(`<p>Полный текст первой статьи.</p><figure><img src="%s" srcset="ignored 2x" sizes="100vw" data-wp-image="1" alt="Иллюстрация"><figcaption>Подпись к иллюстрации</figcaption></figure><script>удалить</script>`, sharedURL),
		},
		{
			Title: "Вторая статья",
			URL:   issueURL + "second/",
			HTML:  fmt.Sprintf(`<h2>Заголовок</h2><p>Полный текст второй статьи.</p><p><img src="%s" alt="Повтор"></p>`, sharedURL),
		},
	}

	fetcher := newTestFetcher(t)
	output := filepath.Join(t.TempDir(), "book.epub")
	if err := BuildEPUB(issue, articles, fetcher, output); err != nil {
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
	for _, name := range []string{"META-INF/container.xml", "EPUB/package.opf", "EPUB/nav.xhtml", "EPUB/css/horizont.css", "EPUB/css/cover.css", "EPUB/xhtml/cover.xhtml", "EPUB/xhtml/contents.xhtml", "EPUB/xhtml/article-001-001.xhtml", "EPUB/xhtml/article-002-001.xhtml", "EPUB/images/cover.png", "EPUB/images/image-001.png"} {
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

func TestBuildEPUBRequiresEveryIssueMaterial(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	err := BuildEPUB(issue, nil, fetcher, filepath.Join(t.TempDir(), "book.epub"))
	if err == nil || !strings.Contains(err.Error(), "article was not fetched") {
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
	defer archive.Close()

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
