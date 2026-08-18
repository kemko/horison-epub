package main

import (
	"os"
	"strings"
	"testing"
)

const issueURL = "https://astra-nova.org/issues/horisont/horisont-n-82/"

func TestParseIssueFixture(t *testing.T) {
	file, err := os.Open("testdata/issue.html")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			t.Errorf("close issue fixture: %v", err)
		}
	}()

	issue, err := ParseIssue(file, issueURL)
	if err != nil {
		t.Fatal(err)
	}
	if issue.Title != "«Горизонт», № 8(82), 2026" {
		t.Fatalf("unexpected issue title: %q", issue.Title)
	}
	if issue.URL != issueURL {
		t.Fatalf("unexpected issue URL: %q", issue.URL)
	}
	if issue.CoverURL != "https://astra-nova.org/wp-content/uploads/2026/08/oblozhka-2.jpg" {
		t.Fatalf("unexpected cover URL: %q", issue.CoverURL)
	}
	if len(issue.Sections) != 4 {
		t.Fatalf("got %d sections, want 4", len(issue.Sections))
	}
	wantSections := []string{
		"Manuscript",
		"Status quo Рассказы современных авторов",
		"Pro et contra Статьи",
		"Post factum Ретрорассказы",
	}
	for i, want := range wantSections {
		if issue.Sections[i].Title != want {
			t.Errorf("section %d = %q, want %q", i, issue.Sections[i].Title, want)
		}
	}
	articleCount := 0
	for _, section := range issue.Sections {
		articleCount += len(section.Articles)
	}
	if articleCount != 22 {
		t.Fatalf("got %d articles, want 22", articleCount)
	}
	first := issue.Sections[0].Articles[0]
	if first.Title != "Лучшие письма читателей" || first.Author != "" {
		t.Fatalf("unexpected first article: %+v", first)
	}
	if !strings.HasPrefix(first.Annotation, "Обзор статей июльского номера.") {
		t.Fatalf("unexpected first annotation: %q", first.Annotation)
	}
	second := issue.Sections[1].Articles[0]
	if second.Author != "Татьяна Тихонова" {
		t.Fatalf("unexpected second author: %q", second.Author)
	}
	if !strings.HasPrefix(second.URL, issueURL) {
		t.Fatalf("unexpected second URL: %q", second.URL)
	}
}

func TestParseIssuePreservesAnnotationSpacingAcrossMarkup(t *testing.T) {
	html := `<h1 class="entry-title">Выпуск</h1><div class="entry-content"><img src="/cover.jpg"><h2>Содержание</h2><h4>Раздел</h4><p>Автор. <a href="/issues/horisont/horisont-n-82/article/">Статья</a><br>Текст <em>с выделением</em> дальше</p></div>`
	issue, err := ParseIssue(strings.NewReader(html), issueURL)
	if err != nil {
		t.Fatal(err)
	}
	if got := issue.Sections[0].Articles[0].Annotation; got != "Текст с выделением дальше" {
		t.Fatalf("annotation = %q", got)
	}
}

func TestParseArticleFixture(t *testing.T) {
	file, err := os.Open("testdata/article.html")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			t.Errorf("close article fixture: %v", err)
		}
	}()

	article, err := ParseArticle(file, issueURL+"лучшие-письма-читателей/")
	if err != nil {
		t.Fatal(err)
	}
	if article.Title != "Лучшие письма читателей" {
		t.Fatalf("unexpected article title: %q", article.Title)
	}
	if article.URL != issueURL+"%D0%BB%D1%83%D1%87%D1%88%D0%B8%D0%B5-%D0%BF%D0%B8%D1%81%D1%8C%D0%BC%D0%B0-%D1%87%D0%B8%D1%82%D0%B0%D1%82%D0%B5%D0%BB%D0%B5%D0%B9/" {
		t.Fatalf("unexpected article URL: %q", article.URL)
	}
	for _, unwanted := range []string{"Вернуться к содержанию", "<script", "<form", "sharedaddy", "jetpack"} {
		if strings.Contains(strings.ToLower(article.HTML), strings.ToLower(unwanted)) {
			t.Errorf("article HTML contains %q", unwanted)
		}
	}
	for _, wanted := range []string{"<p>", "<strong>", "Илья Трейгер", "О. М."} {
		if !strings.Contains(article.HTML, wanted) {
			t.Errorf("article HTML does not contain %q", wanted)
		}
	}
}

func TestParseArticleKeepsAllowedHTMLAndNormalizesURLs(t *testing.T) {
	html := `<html><body><h1 class="entry-title">Тест</h1><div class="entry-content">
<p>Текст <a href="/other">ссылка</a></p><h2>Подзаголовок</h2><ul><li>Пункт</li></ul>
<p>Первая строка<br>Вторая строка</p>
<table><tr><th>Заголовок</th></tr><tr><td>Ячейка</td></tr></table>
<blockquote cite="/source">Цитата</blockquote><figure><img src="/image.png" alt="рисунок"><figcaption>Подпись</figcaption></figure>
<script>alert(1)</script><form><input value="x"></form><p></p><div class="sharedaddy">Реклама</div>
</div></body></html>`
	article, err := ParseArticle(strings.NewReader(html), "https://example.test/issues/item/article/")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`href="https://example.test/other"`,
		`src="https://example.test/image.png"`,
		"<h2>Подзаголовок</h2>", "<ul>", "<table>", "<blockquote", "<figure>", "<figcaption>", "Первая строка<br", "Вторая строка",
	} {
		if !strings.Contains(article.HTML, want) {
			t.Errorf("sanitized HTML does not contain %q: %s", want, article.HTML)
		}
	}
	for _, unwanted := range []string{"script", "form", "input", "sharedaddy", "Реклама", "<p></p>"} {
		if strings.Contains(strings.ToLower(article.HTML), strings.ToLower(unwanted)) {
			t.Errorf("sanitized HTML contains %q: %s", unwanted, article.HTML)
		}
	}
}

func TestParseArticleNormalizesLazyImageURLs(t *testing.T) {
	html := `<h1 class="entry-title">Тест</h1><div class="entry-content"><p>Текст</p>
<img data-src="/data-src.png"><img src="" data-lazy-src="lazy.png">
<img src="data:image/gif;base64,R0lGODlhAQABAIAAAAAAAP///ywAAAAAAQABAAACAUwAOw==" data-src="/fallback.png">
</div>`
	article, err := ParseArticle(strings.NewReader(html), "https://example.test/issues/item/article/")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`src="https://example.test/data-src.png"`,
		`src="https://example.test/issues/item/article/lazy.png"`,
		`src="https://example.test/fallback.png"`,
	} {
		if !strings.Contains(article.HTML, want) {
			t.Errorf("sanitized HTML does not contain %q: %s", want, article.HTML)
		}
	}
	for _, unwanted := range []string{"data-src=", "data-lazy-src=", "data:image"} {
		if strings.Contains(article.HTML, unwanted) {
			t.Errorf("sanitized HTML contains %q: %s", unwanted, article.HTML)
		}
	}
}

func TestParseArticleOnlyRemovesReturnBoilerplate(t *testing.T) {
	html := `<h1 class="entry-title">Тест</h1><div class="entry-content">
<p>Сравните с <a href="../">выпуском целиком</a>, чтобы увидеть контекст.</p>
<p><strong>Вернуться к содержанию номера:</strong> <a href="../">Выпуск</a></p>
</div>`
	article, err := ParseArticle(strings.NewReader(html), "https://example.test/issues/item/article/")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(article.HTML, `href="https://example.test/issues/item/"`) || !strings.Contains(article.HTML, "увидеть контекст") {
		t.Fatalf("ordinary parent-issue link was removed: %s", article.HTML)
	}
	if strings.Contains(strings.ToLower(article.HTML), "вернуться к содержанию") {
		t.Fatalf("return boilerplate was retained: %s", article.HTML)
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		name string
		html string
		want string
	}{
		{name: "title", html: `<div class="entry-content"><h2>Содержание</h2><img src="/cover.jpg"><h4>Раздел</h4><p><a href="/issues/item/article/">Статья</a></p></div>`, want: "missing title"},
		{name: "cover", html: `<h1 class="entry-title">Выпуск</h1><div class="entry-content"><h2>Содержание</h2><h4>Раздел</h4><p><a href="/issues/item/article/">Статья</a></p></div>`, want: "missing cover"},
		{name: "contents", html: `<h1 class="entry-title">Выпуск</h1><div class="entry-content"><img src="/cover.jpg"><h4>Раздел</h4><p><a href="/issues/item/article/">Статья</a></p></div>`, want: "missing contents heading"},
		{name: "materials", html: `<h1 class="entry-title">Выпуск</h1><div class="entry-content"><img src="/cover.jpg"><h2>Содержание</h2><h4>Раздел</h4><p>Нет ссылок</p></div>`, want: "missing materials"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseIssue(strings.NewReader(tt.html), issueURL)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
	if _, err := ParseArticle(strings.NewReader(`<h1 class="entry-title">Тест</h1>`), issueURL+"article/"); err == nil || !strings.Contains(err.Error(), "missing content") {
		t.Fatalf("article error = %v, want missing content", err)
	}
}
