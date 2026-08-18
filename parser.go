package main

import (
	"fmt"
	"io"
	"net/url"
	"path"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/microcosm-cc/bluemonday"
	"golang.org/x/net/html"
)

type Issue struct {
	Title    string
	URL      string
	CoverURL string
	Sections []Section
}

type Section struct {
	Title    string
	Articles []Article
}

type Article struct {
	Title      string
	URL        string
	Author     string
	Annotation string
	HTML       string
}

func ParseIssue(r io.Reader, issueURL string) (Issue, error) {
	base, err := parseHTTPURL(issueURL)
	if err != nil {
		return Issue{}, fmt.Errorf("issue: invalid URL %q: %w", redactURL(issueURL), err)
	}

	doc, err := goquery.NewDocumentFromReader(r)
	if err != nil {
		return Issue{}, fmt.Errorf("issue: parse HTML: %w", err)
	}
	title := normalizeWhitespace(doc.Find(".entry-title").First().Text())
	if title == "" {
		return Issue{}, fmt.Errorf("issue: missing title")
	}
	content := doc.Find(".entry-content").First()
	if len(content.Nodes) == 0 {
		return Issue{}, fmt.Errorf("issue: missing content")
	}

	var contentsHeading *html.Node
	var coverRaw string
	content.Find("img, h1, h2, h3, h4, h5, h6").EachWithBreak(func(_ int, selection *goquery.Selection) bool {
		n := firstNode(selection)
		if isContentsHeading(n) {
			contentsHeading = n
			return false
		}
		if n.Data == "img" && coverRaw == "" {
			coverRaw = attr(n, "src")
			if coverRaw == "" {
				coverRaw = attr(n, "data-src")
			}
		}
		return true
	})
	if contentsHeading == nil {
		return Issue{}, fmt.Errorf("issue: missing contents heading")
	}
	if strings.TrimSpace(coverRaw) == "" {
		return Issue{}, fmt.Errorf("issue: missing cover")
	}
	coverURL, err := resolveHTTPURL(coverRaw, base)
	if err != nil {
		return Issue{}, fmt.Errorf("issue: invalid cover URL %q: %w", redactURL(coverRaw), err)
	}

	issue := Issue{Title: title, URL: base.String(), CoverURL: coverURL.String()}
	contentsSeen := false
	articleCount := 0
	var currentSection Section
	var parseErr error
	appendCurrentSection := func() {
		if len(currentSection.Articles) > 0 {
			issue.Sections = append(issue.Sections, currentSection)
		}
		currentSection = Section{}
	}
	content.Find("h1, h2, h3, h4, h5, h6, p").EachWithBreak(func(_ int, selection *goquery.Selection) bool {
		n := firstNode(selection)
		if isContentsHeading(n) {
			contentsSeen = true
			return true
		}
		if !contentsSeen {
			return true
		}
		switch n.Data {
		case "h4":
			appendCurrentSection()
			currentSection.Title = visibleText(n)
		case "p":
			if currentSection.Title == "" {
				return true
			}
			article, ok := parseContentsArticle(n, base, issuePathPrefix(base))
			if ok {
				articleCount++
				if articleCount > maxArticlesPerIssue {
					parseErr = fmt.Errorf("issue exceeds %d-article limit", maxArticlesPerIssue)
					return false
				}
				currentSection.Articles = append(currentSection.Articles, article)
			}
		}
		return true
	})
	if parseErr != nil {
		return Issue{}, parseErr
	}
	appendCurrentSection()
	if articleCount == 0 {
		return Issue{}, fmt.Errorf("issue: missing materials")
	}
	return issue, nil
}

func ParseArticle(r io.Reader, articleURL string) (Article, error) {
	base, err := parseHTTPURL(articleURL)
	if err != nil {
		return Article{}, fmt.Errorf("article: invalid URL %q: %w", redactURL(articleURL), err)
	}
	doc, err := goquery.NewDocumentFromReader(r)
	if err != nil {
		return Article{}, fmt.Errorf("article: parse HTML: %w", err)
	}
	title := normalizeWhitespace(doc.Find(".entry-title").First().Text())
	if title == "" {
		return Article{}, fmt.Errorf("article: missing title")
	}
	content := doc.Find(".entry-content").First()
	if len(content.Nodes) == 0 {
		return Article{}, fmt.Errorf("article: missing content")
	}

	normalizeContentURLs(content, base)
	removeReturnLink(content, base)
	removeServiceMarkup(content)
	removeEmptyNodes(content)

	if len(content.Nodes) == 0 || strings.TrimSpace(visibleText(content.Nodes[0])) == "" {
		return Article{}, fmt.Errorf("article: missing text")
	}
	body, err := content.Html()
	if err != nil {
		return Article{}, fmt.Errorf("article: serialize content: %w", err)
	}
	body = sanitizeHTML(body)
	if strings.TrimSpace(stripHTMLText(body)) == "" {
		return Article{}, fmt.Errorf("article: missing text")
	}
	return Article{Title: title, URL: base.String(), HTML: body}, nil
}

func parseContentsArticle(p *html.Node, base *url.URL, prefix string) (Article, bool) {
	links := goquery.NewDocumentFromNode(p).Find("a[href]")
	var link *html.Node
	var articleURL *url.URL
	links.EachWithBreak(func(_ int, selection *goquery.Selection) bool {
		n := firstNode(selection)
		candidate, err := resolveHTTPURL(attr(n, "href"), base)
		if err == nil && candidate.Scheme == base.Scheme && strings.EqualFold(candidate.Host, base.Host) && strings.HasPrefix(candidate.EscapedPath(), prefix) {
			link = n
			articleURL = candidate
			return false
		}
		return true
	})
	if link == nil || articleURL == nil {
		return Article{}, false
	}

	author := strings.TrimSpace(collectTextBefore(p, link))
	author = normalizeWhitespace(author)
	author = strings.TrimSuffix(author, ".")
	annotation := ""
	if br := goquery.NewDocumentFromNode(p).Find("br").First(); len(br.Nodes) > 0 {
		annotation = normalizeWhitespace(collectTextAfter(p, br.Nodes[0]))
	}
	return Article{
		Title:      visibleText(link),
		URL:        articleURL.String(),
		Author:     author,
		Annotation: annotation,
	}, true
}

func parseHTTPURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("unsupported scheme %q", u.Scheme)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("missing host")
	}
	if u.User != nil {
		return nil, fmt.Errorf("URL userinfo is not allowed")
	}
	return u, nil
}

func redactURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "<invalid URL>"
	}
	u.User = nil
	return u.String()
}

func resolveHTTPURL(raw string, base *url.URL) (*url.URL, error) {
	ref, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, err
	}
	resolved := base.ResolveReference(ref)
	return parseHTTPURL(resolved.String())
}

func issuePathPrefix(base *url.URL) string {
	prefix := base.EscapedPath()
	if prefix == "" {
		prefix = "/"
	}
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	return prefix
}

func firstNode(selection *goquery.Selection) *html.Node {
	if selection == nil || len(selection.Nodes) == 0 {
		return nil
	}
	return selection.Nodes[0]
}

func isContentsHeading(n *html.Node) bool {
	if n == nil || n.Type != html.ElementNode || n.Data != "h2" && n.Data != "h3" && n.Data != "h4" && n.Data != "h5" && n.Data != "h6" {
		return false
	}
	return strings.HasPrefix(strings.ToLower(visibleText(n)), "содержание")
}

func normalizeContentURLs(content *goquery.Selection, base *url.URL) {
	content.Find("a[href]").Each(func(_ int, selection *goquery.Selection) {
		n := firstNode(selection)
		if resolved, err := resolveHTTPURL(attr(n, "href"), base); err == nil {
			setAttr(n, "href", resolved.String())
		} else {
			removeAttr(n, "href")
		}
	})
	content.Find("img").Each(func(_ int, selection *goquery.Selection) {
		n := firstNode(selection)
		resolvedSrc := ""
		for _, name := range []string{"src", "data-src", "data-lazy-src"} {
			raw := strings.TrimSpace(attr(n, name))
			if raw == "" {
				continue
			}
			if resolved, err := resolveHTTPURL(raw, base); err == nil {
				resolvedSrc = resolved.String()
				break
			}
		}
		removeAttr(n, "src")
		if resolvedSrc != "" {
			setAttr(n, "src", resolvedSrc)
		}
	})
}

func removeReturnLink(content *goquery.Selection, articleBase *url.URL) {
	parentIssue := *articleBase
	articlePath := strings.TrimSuffix(parentIssue.Path, "/")
	parentIssue.Path = path.Dir(articlePath) + "/"
	parentIssue.RawPath = ""

	content.Find("a").Each(func(_ int, selection *goquery.Selection) {
		n := firstNode(selection)
		linkIsReturn := strings.Contains(strings.ToLower(visibleText(n)), "вернуться к содержанию")
		for parent := n.Parent; parent != nil; parent = parent.Parent {
			if parent.Data == "p" {
				paragraphIsReturn := strings.HasPrefix(strings.ToLower(visibleText(parent)), "вернуться к содержанию")
				candidate, err := resolveHTTPURL(attr(n, "href"), articleBase)
				linksToIssue := err == nil && candidate.Scheme == parentIssue.Scheme && candidate.Host == parentIssue.Host && candidate.EscapedPath() == parentIssue.EscapedPath()
				if paragraphIsReturn && (linkIsReturn || linksToIssue) {
					detachNode(parent)
				} else if linkIsReturn {
					detachNode(n)
				}
				return
			}
		}
		if linkIsReturn {
			detachNode(n)
		}
	})
}

func removeServiceMarkup(content *goquery.Selection) {
	content.Find("script, style, noscript, form, iframe, video, audio, [class*='sharedaddy'], [class*='jetpack'], [id^='jp-'], [id*='jetpack'], [id^='atatags-'], .jp-relatedposts").Each(func(_ int, selection *goquery.Selection) {
		n := firstNode(selection)
		detachNode(n)
	})
}

func removeEmptyNodes(content *goquery.Selection) {
	content.Find("p, div, section, aside, nav, header, footer").Each(func(_ int, selection *goquery.Selection) {
		n := firstNode(selection)
		if !hasMeaningfulContent(n) {
			detachNode(n)
		}
	})
}

func hasMeaningfulContent(n *html.Node) bool {
	if n.Type == html.TextNode {
		return strings.TrimSpace(n.Data) != ""
	}
	if n.Type == html.ElementNode {
		switch n.Data {
		case "img", "table", "ul", "ol", "blockquote", "figure", "pre", "h1", "h2", "h3", "h4", "h5", "h6":
			return true
		}
	}
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		if hasMeaningfulContent(child) {
			return true
		}
	}
	return false
}

func sanitizeHTML(body string) string {
	policy := bluemonday.NewPolicy()
	policy.AllowElements("p", "h1", "h2", "h3", "h4", "h5", "h6", "ul", "ol", "li", "table", "thead", "tbody", "tfoot", "tr", "th", "td", "blockquote", "figure", "figcaption", "a", "img", "strong", "em", "b", "i", "u", "s", "br", "code", "pre", "sub", "sup", "q", "cite")
	policy.AllowAttrs("href", "title", "rel").OnElements("a")
	policy.AllowAttrs("src", "alt", "title", "width", "height").OnElements("img")
	policy.AllowAttrs("cite").OnElements("blockquote", "q")
	policy.AllowAttrs("colspan", "rowspan", "scope").OnElements("th", "td")
	policy.AllowURLSchemes("http", "https")
	policy.AllowRelativeURLs(true)
	return policy.Sanitize(body)
}

func stripHTMLText(body string) string {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(body))
	if err != nil {
		return ""
	}
	return visibleText(doc.Nodes[0])
}

func visibleText(n *html.Node) string {
	var b strings.Builder
	appendVisibleText(&b, n)
	return normalizeWhitespace(b.String())
}

func appendVisibleText(builder *strings.Builder, node *html.Node) {
	if node.Type == html.TextNode {
		builder.WriteString(node.Data)
		return
	}
	if node.Type == html.ElementNode && node.Data == "br" {
		builder.WriteByte('\n')
		return
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		appendVisibleText(builder, child)
	}
}

func collectTextBefore(root, target *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node) bool
	walk = func(node *html.Node) bool {
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			if child == target {
				return true
			}
			switch {
			case child.Type == html.TextNode:
				b.WriteString(child.Data)
			case child.Type == html.ElementNode && child.Data == "br":
				b.WriteByte('\n')
			default:
				if walk(child) {
					return true
				}
			}
		}
		return false
	}
	walk(root)
	return b.String()
}

func collectTextAfter(root, target *html.Node) string {
	var b strings.Builder
	found := false
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			if found {
				appendVisibleText(&b, child)
				continue
			}
			if child == target {
				found = true
				continue
			}
			walk(child)
		}
	}
	walk(root)
	return b.String()
}

func normalizeWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func attr(n *html.Node, name string) string {
	for _, attribute := range n.Attr {
		if attribute.Key == name {
			return attribute.Val
		}
	}
	return ""
}

func setAttr(n *html.Node, name, value string) {
	for i := range n.Attr {
		if n.Attr[i].Key == name {
			n.Attr[i].Val = value
			return
		}
	}
	n.Attr = append(n.Attr, html.Attribute{Key: name, Val: value})
}

func removeAttr(n *html.Node, name string) {
	for i := range n.Attr {
		if n.Attr[i].Key == name {
			n.Attr = append(n.Attr[:i], n.Attr[i+1:]...)
			return
		}
	}
}

func detachNode(n *html.Node) {
	if n != nil && n.Parent != nil {
		n.Parent.RemoveChild(n)
	}
}
