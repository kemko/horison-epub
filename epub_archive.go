package main

import (
	"archive/zip"
	"encoding/xml"
	"errors"
	"fmt"
	stdhtml "html"
	"io"
	"path"
	"strings"
)

const (
	maxEPUBMetadataBytes = 16 << 20
	maxEPUBEntryBytes    = 128 << 20
)

type epubNavigationEntry struct {
	title    string
	href     string
	children []epubNavigationEntry
}

func writeValidatedEPUB(source io.ReaderAt, size int64, output io.Writer, issue Issue, coverPath string) error {
	archive, err := zip.NewReader(source, size)
	if err != nil {
		return fmt.Errorf("open ZIP: %w", err)
	}
	if err := validateEPUBArchive(archive, expectedEPUBEntries(issue, coverPath)); err != nil {
		return err
	}

	replacements := map[string][]byte{
		"EPUB/nav.xhtml": renderEPUBNavigation(issue),
		"EPUB/toc.ncx":   renderEPUBNCX(issue),
	}
	writer := zip.NewWriter(output)
	for _, file := range archive.File {
		header := &zip.FileHeader{Name: file.Name, Method: file.Method}
		if file.Name != "mimetype" {
			header.SetMode(file.Mode())
			header.Modified = file.Modified
		}
		destination, err := writer.CreateHeader(header)
		if err != nil {
			return errors.Join(fmt.Errorf("create ZIP entry %q: %w", file.Name, err), writer.Close())
		}
		if body, ok := replacements[file.Name]; ok {
			if _, err := destination.Write(body); err != nil {
				return errors.Join(fmt.Errorf("write ZIP entry %q: %w", file.Name, err), writer.Close())
			}
			continue
		}

		reader, err := file.Open()
		if err != nil {
			return errors.Join(fmt.Errorf("open ZIP entry %q: %w", file.Name, err), writer.Close())
		}
		_, copyErr := io.CopyN(destination, reader, int64(file.UncompressedSize))
		var extra [1]byte
		extraBytes, finalReadErr := reader.Read(extra[:])
		if extraBytes != 0 || !errors.Is(finalReadErr, io.EOF) {
			copyErr = errors.Join(copyErr, fmt.Errorf("unexpected uncompressed size"), finalReadErr)
		}
		closeErr := reader.Close()
		if err := errors.Join(copyErr, closeErr); err != nil {
			return errors.Join(fmt.Errorf("copy ZIP entry %q: %w", file.Name, err), writer.Close())
		}
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("close ZIP: %w", err)
	}
	return nil
}

func validateEPUBArchive(archive *zip.Reader, expected map[string]struct{}) error {
	if len(archive.File) == 0 || archive.File[0].Name != "mimetype" || archive.File[0].Method != zip.Store {
		return fmt.Errorf("mimetype must be the first uncompressed ZIP entry")
	}

	files := make(map[string]*zip.File, len(archive.File))
	for _, file := range archive.File {
		if file.UncompressedSize64 > maxEPUBEntryBytes {
			return fmt.Errorf("ZIP entry %q exceeds size limit", file.Name)
		}
		if file.Name != path.Clean(file.Name) || path.IsAbs(file.Name) || strings.Contains(file.Name, "\\") || strings.HasPrefix(file.Name, "../") {
			return fmt.Errorf("unsafe ZIP entry %q", file.Name)
		}
		if _, exists := files[file.Name]; exists {
			return fmt.Errorf("duplicate ZIP entry %q", file.Name)
		}
		files[file.Name] = file
	}
	for name := range expected {
		if files[name] == nil {
			return fmt.Errorf("missing ZIP entry %q", name)
		}
	}

	mimetype, err := readEPUBEntry(files["mimetype"])
	if err != nil {
		return err
	}
	if string(mimetype) != "application/epub+zip" {
		return fmt.Errorf("invalid EPUB mimetype")
	}
	packageData, err := readEPUBEntry(files["EPUB/package.opf"])
	if err != nil {
		return err
	}
	var packageDocument struct {
		Items []struct {
			Href string `xml:"href,attr"`
		} `xml:"manifest>item"`
	}
	if err := xml.Unmarshal(packageData, &packageDocument); err != nil {
		return fmt.Errorf("parse package manifest: %w", err)
	}
	for _, item := range packageDocument.Items {
		href := strings.TrimSpace(item.Href)
		if href == "" || strings.Contains(href, "\\") || strings.HasPrefix(href, "/") {
			return fmt.Errorf("invalid package manifest href %q", item.Href)
		}
		name := path.Clean(path.Join("EPUB", href))
		if !strings.HasPrefix(name, "EPUB/") || files[name] == nil {
			return fmt.Errorf("package manifest href %q does not resolve", item.Href)
		}
	}
	return nil
}

func readEPUBEntry(file *zip.File) ([]byte, error) {
	if file == nil {
		return nil, fmt.Errorf("missing ZIP entry")
	}
	reader, err := file.Open()
	if err != nil {
		return nil, fmt.Errorf("open ZIP entry %q: %w", file.Name, err)
	}
	body, readErr := io.ReadAll(io.LimitReader(reader, maxEPUBMetadataBytes+1))
	closeErr := reader.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, fmt.Errorf("read ZIP entry %q: %w", file.Name, err)
	}
	if len(body) > maxEPUBMetadataBytes {
		return nil, fmt.Errorf("ZIP entry %q exceeds metadata limit", file.Name)
	}
	return body, nil
}

func expectedEPUBEntries(issue Issue, coverPath string) map[string]struct{} {
	expected := map[string]struct{}{
		"mimetype":                                     {},
		"META-INF/container.xml":                       {},
		"EPUB/package.opf":                             {},
		"EPUB/nav.xhtml":                               {},
		"EPUB/toc.ncx":                                 {},
		"EPUB/css/horisont.css":                        {},
		"EPUB/css/cover.css":                           {},
		"EPUB/xhtml/cover.xhtml":                       {},
		"EPUB/xhtml/contents.xhtml":                    {},
		path.Clean(path.Join("EPUB/xhtml", coverPath)): {},
	}
	for sectionIndex, section := range issue.Sections {
		expected[fmt.Sprintf("EPUB/xhtml/section-%03d.xhtml", sectionIndex+1)] = struct{}{}
		for articleIndex := range section.Articles {
			expected[fmt.Sprintf("EPUB/xhtml/article-%03d-%03d.xhtml", sectionIndex+1, articleIndex+1)] = struct{}{}
		}
	}
	return expected
}

func epubNavigation(issue Issue) []epubNavigationEntry {
	entries := []epubNavigationEntry{{title: "Содержание", href: "xhtml/contents.xhtml"}}
	for sectionIndex, section := range issue.Sections {
		entry := epubNavigationEntry{
			title: section.Title,
			href:  fmt.Sprintf("xhtml/section-%03d.xhtml", sectionIndex+1),
		}
		for articleIndex, article := range section.Articles {
			entry.children = append(entry.children, epubNavigationEntry{
				title: article.Title,
				href:  fmt.Sprintf("xhtml/article-%03d-%03d.xhtml", sectionIndex+1, articleIndex+1),
			})
		}
		entries = append(entries, entry)
	}
	return entries
}

func renderEPUBNavigation(issue Issue) []byte {
	var builder strings.Builder
	builder.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<!DOCTYPE html>\n")
	builder.WriteString(`<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops"><head><title dir="auto">`)
	builder.WriteString(stdhtml.EscapeString(issue.Title))
	builder.WriteString(`</title></head><body dir="auto"><nav epub:type="toc"><h1>Table of Contents</h1><ol>`)
	writeEPUBNavigationItems(&builder, epubNavigation(issue))
	builder.WriteString("</ol></nav></body></html>\n")
	return []byte(builder.String())
}

func writeEPUBNavigationItems(builder *strings.Builder, entries []epubNavigationEntry) {
	for _, entry := range entries {
		fmt.Fprintf(builder, `<li><a href="%s">%s</a>`, entry.href, stdhtml.EscapeString(entry.title))
		if len(entry.children) > 0 {
			builder.WriteString("<ol>")
			writeEPUBNavigationItems(builder, entry.children)
			builder.WriteString("</ol>")
		}
		builder.WriteString("</li>")
	}
}

func renderEPUBNCX(issue Issue) []byte {
	var builder strings.Builder
	builder.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
	builder.WriteString(`<ncx xmlns="http://www.daisy.org/z3986/2005/ncx/" version="2005-1"><head><meta name="dtb:uid" content="`)
	builder.WriteString(stdhtml.EscapeString(issue.URL))
	builder.WriteString(`"></meta><meta name="dtb:depth" content="2"></meta><meta name="dtb:totalPageCount" content="0"></meta><meta name="dtb:maxPageNumber" content="0"></meta></head><docTitle><text>`)
	builder.WriteString(stdhtml.EscapeString(issue.Title))
	builder.WriteString("</text></docTitle><docAuthor><text></text></docAuthor><navMap>")
	index := 0
	writeEPUBNCXPoints(&builder, epubNavigation(issue), &index)
	builder.WriteString("</navMap></ncx>\n")
	return []byte(builder.String())
}

func writeEPUBNCXPoints(builder *strings.Builder, entries []epubNavigationEntry, index *int) {
	for _, entry := range entries {
		*index++
		fmt.Fprintf(builder, `<navPoint id="navPoint-%d" playOrder="%d"><navLabel><text>%s</text></navLabel><content src="%s"></content>`, *index, *index, stdhtml.EscapeString(entry.title), entry.href)
		writeEPUBNCXPoints(builder, entry.children, index)
		builder.WriteString("</navPoint>")
	}
}
