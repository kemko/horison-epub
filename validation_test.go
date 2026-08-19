package main

import (
	"archive/zip"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestEndToEndBuildsAutonomousEPUB(t *testing.T) {
	var sharedImageRequests atomic.Int32
	server := newValidationServer(t, validationServerOK, &sharedImageRequests)
	defer server.Close()

	output := filepath.Join(t.TempDir(), "issue.epub")
	var stdout strings.Builder
	if err := run([]string{"-allow-private-network", "-o", output, server.URL + "/issues/demo/"}, &stdout, io.Discard); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(stdout.String()) != output {
		t.Fatalf("stdout = %q, want %q", stdout.String(), output)
	}
	if sharedImageRequests.Load() != 1 {
		t.Fatalf("shared image requests = %d, want 1", sharedImageRequests.Load())
	}

	entries := readZipEntries(t, output)
	if string(entries["mimetype"].body) != "application/epub+zip" || entries["mimetype"].method != zip.Store {
		t.Fatal("EPUB mimetype is not stored correctly")
	}
	for _, name := range []string{
		"META-INF/container.xml",
		"EPUB/package.opf",
		"EPUB/nav.xhtml",
		"EPUB/xhtml/cover.xhtml",
		"EPUB/images/cover.png",
		"EPUB/images/image-001.png",
		"EPUB/css/horisont.css",
	} {
		if _, ok := entries[name]; !ok {
			t.Errorf("missing archive entry %q", name)
		}
	}
	if strings.Count(string(entries["EPUB/package.opf"].body), "image-001.png") != 1 {
		t.Fatal("shared image is not represented by one manifest item")
	}

	nav := string(entries["EPUB/nav.xhtml"].body)
	for _, want := range []string{"Проза", "Литература", "Первая статья", "Вторая статья", "Третья статья"} {
		if !strings.Contains(nav, want) {
			t.Errorf("nav.xhtml does not contain %q", want)
		}
	}
	contents := string(entries["EPUB/xhtml/contents.xhtml"].body)
	for _, want := range []string{
		"Автор Один",
		"Аннотация первой",
		"Автор Два",
		"Аннотация второй",
		"Автор Три",
		"Аннотация третьей",
	} {
		if !strings.Contains(contents, want) {
			t.Errorf("contents.xhtml does not contain %q", want)
		}
	}

	articleCount := 0
	for name, entry := range entries {
		if !strings.HasPrefix(name, "EPUB/xhtml/article-") {
			continue
		}
		articleCount++
		body := string(entry.body)
		if strings.Contains(body, `src="http://`) || strings.Contains(body, `src="https://`) {
			t.Errorf("article %q contains an external image source", name)
		}
		for _, unwanted := range []string{"srcset", "sizes", "data-wp-image", "<script", "<iframe"} {
			if strings.Contains(body, unwanted) {
				t.Errorf("article %q contains removed value %q", name, unwanted)
			}
		}
	}
	if articleCount != 3 {
		t.Fatalf("article XHTML count = %d, want 3", articleCount)
	}
	for _, want := range []string{"Полный текст первой статьи", "Полный текст второй статьи", "Полный текст третьей статьи"} {
		found := false
		for name, entry := range entries {
			if strings.HasPrefix(name, "EPUB/xhtml/article-") && strings.Contains(string(entry.body), want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("no article contains %q", want)
		}
	}
}

func TestEndToEndFailureLeavesNoPartialEPUB(t *testing.T) {
	for _, test := range []struct {
		name string
		mode validationServerMode
	}{
		{name: "article", mode: validationServerArticleFailure},
		{name: "image", mode: validationServerImageFailure},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := newValidationServer(t, test.mode, nil)
			defer server.Close()

			directory := t.TempDir()
			output := filepath.Join(directory, "failed.epub")
			err := run([]string{"-allow-private-network", "-o", output, server.URL + "/issues/demo/"}, &strings.Builder{}, &strings.Builder{})
			if err == nil {
				t.Fatal("run succeeded")
			}
			if _, statErr := os.Stat(output); !os.IsNotExist(statErr) {
				t.Fatalf("output stat error = %v", statErr)
			}
			entries, readErr := os.ReadDir(directory)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if len(entries) != 0 {
				t.Fatalf("partial files remain: %v", entries)
			}
		})
	}
}

type validationServerMode int

const (
	validationServerOK validationServerMode = iota
	validationServerArticleFailure
	validationServerImageFailure
)

func newValidationServer(t *testing.T, mode validationServerMode, sharedImageRequests *atomic.Int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/issues/demo/":
			writeHTML(w, validationIssueHTML())
		case "/issues/demo/one/":
			writeHTML(w, validationArticleHTML("Первая статья", "Полный текст первой статьи", "/assets/shared.png"))
		case "/issues/demo/two/":
			if mode == validationServerArticleFailure {
				http.NotFound(w, r)
				return
			}
			imagePath := "/assets/shared.png"
			if mode == validationServerImageFailure {
				imagePath = "/assets/missing.png"
			}
			writeHTML(w, validationArticleHTML("Вторая статья", "Полный текст второй статьи", imagePath))
		case "/issues/demo/three/":
			writeHTML(w, validationArticleHTML("Третья статья", "Полный текст третьей статьи", "/assets/shared.png"))
		case "/assets/cover.png":
			writeImage(w, pngBytes())
		case "/assets/shared.png":
			if sharedImageRequests != nil {
				sharedImageRequests.Add(1)
			}
			writeImage(w, pngBytes())
		default:
			http.NotFound(w, r)
		}
	}))
}

func validationIssueHTML() string {
	return `<h1 class="entry-title">Сквозной выпуск</h1><div class="entry-content"><img src="/assets/cover.png" alt="Обложка"><h2>Содержание</h2><h4>Проза</h4><p>Автор Один. <a href="/issues/demo/one/">Первая статья</a><br>Аннотация первой.</p><p>Автор Два. <a href="/issues/demo/two/">Вторая статья</a><br>Аннотация второй.</p><h4>Литература</h4><p>Автор Три. <a href="/issues/demo/three/">Третья статья</a><br>Аннотация третьей.</p></div>`
}

func validationArticleHTML(title, text, imagePath string) string {
	return fmt.Sprintf(`<h1 class="entry-title">%s</h1><div class="entry-content"><p>%s</p><figure><img src="%s" srcset="ignored 2x" sizes="100vw" data-wp-image="1" alt="Иллюстрация"><figcaption>Подпись к иллюстрации</figcaption></figure><script>удалить</script><iframe src="/video"></iframe></div>`, title, text, imagePath)
}

func writeHTML(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = fmt.Fprint(w, body)
}

func writeImage(w http.ResponseWriter, body []byte) {
	w.Header().Set("Content-Type", "image/png")
	_, _ = w.Write(body)
}
