package main

import (
	"encoding/base64"
	"fmt"
	stdhtml "html"
	"path/filepath"
	"strings"

	"github.com/PuerkitoBio/goquery"
	goepub "github.com/go-shiori/go-epub"
)

const epubCSS = `body {
  color: #222;
  font-family: serif;
  line-height: 1.45;
  margin: 5%;
}
h1, h2, h3, h4 { line-height: 1.2; }
.contents ul, .contents ol { padding-left: 1.4em; }
.contents li { margin: 0.55em 0; }
.annotation { margin: 0.2em 0 1em; }
.author { font-style: italic; }
table { border-collapse: collapse; margin: 1em 0; max-width: 100%; }
th, td { border: 1px solid #888; padding: 0.3em; }
blockquote { border-left: 0.25em solid #aaa; margin: 1em 0; padding-left: 1em; }
figure { margin: 1em 0; text-align: center; }
figcaption { font-size: 0.9em; font-style: italic; }
img { height: auto; max-width: 100%; }
`

type epubArticlePlan struct {
	summary Article
	body    string
	file    string
}

type epubSectionPlan struct {
	section Section
	file    string
	body    []epubArticlePlan
}

// BuildEPUB writes a complete EPUB from a parsed issue and its parsed articles.
// Articles are matched by URL so callers may fetch and parse them sequentially.
func BuildEPUB(issue Issue, articles []Article, fetcher *Fetcher, outputPath string) error {
	if fetcher == nil {
		return fmt.Errorf("epub: missing fetcher")
	}
	if strings.TrimSpace(outputPath) == "" {
		return fmt.Errorf("epub: missing output path")
	}
	if issue.Title == "" || issue.URL == "" || issue.CoverURL == "" {
		return fmt.Errorf("epub: incomplete issue metadata")
	}

	articleByURL := make(map[string]Article, len(articles))
	for _, article := range articles {
		key, _, err := canonicalURL(article.URL)
		if err != nil {
			return fmt.Errorf("epub: article %q: invalid URL: %w", article.URL, err)
		}
		articleByURL[key] = article
	}

	epubFile, err := goepub.NewEpub(issue.Title)
	if err != nil {
		return fmt.Errorf("epub: create: %w", err)
	}
	epubFile.SetLang("ru")
	epubFile.SetIdentifier(issue.URL)

	cssPath, err := epubFile.AddCSS(cssDataURL(epubCSS), "horizont.css")
	if err != nil {
		return fmt.Errorf("epub: add CSS: %w", err)
	}
	resources := &epubResources{
		epub:    epubFile,
		fetcher: fetcher,
		paths:   make(map[string]string),
	}
	coverPath, err := resources.add(issue.CoverURL, "cover")
	if err != nil {
		return fmt.Errorf("epub: cover: %w", err)
	}

	sections := make([]epubSectionPlan, 0, len(issue.Sections))
	for sectionIndex, section := range issue.Sections {
		plan := epubSectionPlan{
			section: section,
			file:    fmt.Sprintf("section-%03d.xhtml", sectionIndex+1),
			body:    make([]epubArticlePlan, 0, len(section.Articles)),
		}
		for articleIndex, summary := range section.Articles {
			key, _, err := canonicalURL(summary.URL)
			if err != nil {
				return fmt.Errorf("epub: material %q: invalid URL: %w", summary.Title, err)
			}
			article, ok := articleByURL[key]
			if !ok {
				return fmt.Errorf("epub: material %q: article was not fetched", summary.Title)
			}
			if article.Title == "" {
				article.Title = summary.Title
			}
			if article.Author == "" {
				article.Author = summary.Author
			}
			if strings.TrimSpace(article.HTML) == "" {
				return fmt.Errorf("epub: material %q: missing text", summary.Title)
			}
			body, err := renderArticleBody(article, resources)
			if err != nil {
				return fmt.Errorf("epub: material %q: %w", summary.Title, err)
			}
			plan.body = append(plan.body, epubArticlePlan{
				summary: summary,
				body:    body,
				file:    fmt.Sprintf("article-%03d-%03d.xhtml", sectionIndex+1, articleIndex+1),
			})
		}
		sections = append(sections, plan)
	}

	contents := renderContents(sections)
	if _, err := epubFile.AddSection(contents, "Содержание", "contents.xhtml", cssPath); err != nil {
		return fmt.Errorf("epub: add contents: %w", err)
	}
	for _, section := range sections {
		sectionBody := fmt.Sprintf("<h1>%s</h1>", stdhtml.EscapeString(section.section.Title))
		if _, err := epubFile.AddSection(sectionBody, section.section.Title, section.file, cssPath); err != nil {
			return fmt.Errorf("epub: add section %q: %w", section.section.Title, err)
		}
		for _, article := range section.body {
			if _, err := epubFile.AddSubSection(section.file, article.body, article.summary.Title, article.file, cssPath); err != nil {
				return fmt.Errorf("epub: add material %q: %w", article.summary.Title, err)
			}
		}
	}
	if err := epubFile.SetCover(coverPath, ""); err != nil {
		return fmt.Errorf("epub: set cover: %w", err)
	}
	if err := epubFile.Write(outputPath); err != nil {
		return fmt.Errorf("epub: write %q: %w", outputPath, err)
	}
	return nil
}

type epubResources struct {
	epub    *goepub.Epub
	fetcher *Fetcher
	paths   map[string]string
	next    int
}

func (r *epubResources) add(rawURL, name string) (string, error) {
	key, _, err := canonicalURL(rawURL)
	if err != nil {
		return "", err
	}
	if path, ok := r.paths[key]; ok {
		return path, nil
	}
	image, err := r.fetcher.FetchImage(rawURL)
	if err != nil {
		return "", err
	}
	finalKey, _, err := canonicalURL(image.URL)
	if err != nil {
		return "", fmt.Errorf("invalid final image URL: %w", err)
	}
	if path, ok := r.paths[finalKey]; ok {
		r.paths[key] = path
		return path, nil
	}

	extension := filepath.Ext(image.Path)
	if name == "" {
		r.next++
		name = fmt.Sprintf("image-%03d", r.next)
	}
	if filepath.Ext(name) == "" {
		name += extension
	}
	path, err := r.epub.AddImage(image.Path, name)
	if err != nil {
		return "", fmt.Errorf("add image: %w", err)
	}
	r.paths[key] = path
	r.paths[finalKey] = path
	return path, nil
}

func renderContents(plans []epubSectionPlan) string {
	var builder strings.Builder
	builder.WriteString(`<h1>Содержание</h1><div class="contents">`)
	for _, section := range plans {
		fmt.Fprintf(&builder, "<h2>%s</h2><ol>", stdhtml.EscapeString(section.section.Title))
		for _, article := range section.body {
			fmt.Fprintf(&builder, `<li><a href="%s">%s</a>`, stdhtml.EscapeString(article.file), stdhtml.EscapeString(article.summary.Title))
			if author := strings.TrimSpace(article.summary.Author); author != "" {
				fmt.Fprintf(&builder, " — %s", stdhtml.EscapeString(author))
			}
			if annotation := strings.TrimSpace(article.summary.Annotation); annotation != "" {
				fmt.Fprintf(&builder, `<p class="annotation">%s</p>`, stdhtml.EscapeString(annotation))
			}
			builder.WriteString("</li>")
		}
		builder.WriteString("</ol>")
	}
	builder.WriteString("</div>")
	return builder.String()
}

func renderArticleBody(article Article, resources *epubResources) (string, error) {
	body := sanitizeHTML(article.HTML)
	body, err := rewriteArticleImages(body, resources)
	if err != nil {
		return "", err
	}

	var builder strings.Builder
	fmt.Fprintf(&builder, "<h1>%s</h1>", stdhtml.EscapeString(article.Title))
	if author := strings.TrimSpace(article.Author); author != "" {
		fmt.Fprintf(&builder, `<p class="author">%s</p>`, stdhtml.EscapeString(author))
	}
	builder.WriteString(body)
	return builder.String(), nil
}

func rewriteArticleImages(body string, resources *epubResources) (string, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("parse text: %w", err)
	}
	content := doc.Find("body").First()
	if len(content.Nodes) == 0 {
		return "", fmt.Errorf("parse text: missing body")
	}
	var firstErr error
	content.Find("img").Each(func(_ int, selection *goquery.Selection) {
		if firstErr != nil {
			return
		}
		n := firstNode(selection)
		src := attr(n, "src")
		if strings.TrimSpace(src) == "" {
			firstErr = fmt.Errorf("image: missing src")
			return
		}
		path, err := resources.add(src, "")
		if err != nil {
			firstErr = fmt.Errorf("image %q: %w", src, err)
			return
		}
		setAttr(n, "src", path)
		removeAttr(n, "srcset")
		removeAttr(n, "sizes")
		for i := len(n.Attr) - 1; i >= 0; i-- {
			if strings.HasPrefix(strings.ToLower(n.Attr[i].Key), "data-") {
				n.Attr = append(n.Attr[:i], n.Attr[i+1:]...)
			}
		}
	})
	if firstErr != nil {
		return "", firstErr
	}
	result, err := content.Html()
	if err != nil {
		return "", fmt.Errorf("serialize text: %w", err)
	}
	return result, nil
}

func cssDataURL(css string) string {
	return "data:text/css;base64," + base64.StdEncoding.EncodeToString([]byte(css))
}
