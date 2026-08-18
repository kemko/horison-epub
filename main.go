package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) (resultErr error) {
	flags := flag.NewFlagSet("horizont-epub", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	output := flags.String("o", "", "output EPUB path")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			if _, err := fmt.Fprintln(stderr, "usage: horizont-epub [-o output.epub] <issue-url>"); err != nil {
				return fmt.Errorf("write help: %w", err)
			}
			return nil
		}
		return fmt.Errorf("invalid arguments: %w", err)
	}
	if flags.NArg() != 1 {
		return errors.New("usage: horizont-epub [-o output.epub] <issue-url>")
	}

	issueURL := flags.Arg(0)
	if _, err := parseHTTPURL(issueURL); err != nil {
		return fmt.Errorf("invalid issue URL %q: %w", issueURL, err)
	}
	outputPath, err := outputPathFor(issueURL, *output)
	if err != nil {
		return err
	}
	if err := refuseExistingOutput(outputPath); err != nil {
		return err
	}

	fetcher, err := NewFetcher()
	if err != nil {
		return fmt.Errorf("create fetcher: %w", err)
	}
	defer func() {
		if err := fetcher.Close(); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("remove image temporary directory: %w", err))
		}
	}()

	issueBody, finalIssueURL, err := fetcher.FetchHTML(issueURL)
	if err != nil {
		return err
	}
	issue, err := ParseIssue(bytes.NewReader(issueBody), finalIssueURL)
	if err != nil {
		return fmt.Errorf("parse issue %s: %w", finalIssueURL, err)
	}

	var articles []Article
	for _, section := range issue.Sections {
		for _, summary := range section.Articles {
			body, finalArticleURL, err := fetcher.FetchHTML(summary.URL)
			if err != nil {
				return err
			}
			article, err := ParseArticle(bytes.NewReader(body), finalArticleURL)
			if err != nil {
				return fmt.Errorf("parse article %s: %w", summary.URL, err)
			}
			// Keep the issue URL as the stable lookup key; ParseArticle used the
			// redirected URL above so relative resources resolve from the final page.
			article.URL = summary.URL
			articles = append(articles, article)
		}
	}

	return writeEPUB(issue, articles, fetcher, outputPath, stdout)
}

func outputPathFor(issueURL, requested string) (string, error) {
	if strings.TrimSpace(requested) != "" {
		return requested, nil
	}
	u, err := parseHTTPURL(issueURL)
	if err != nil {
		return "", fmt.Errorf("invalid issue URL %q: %w", issueURL, err)
	}
	segment := path.Base(strings.TrimRight(u.EscapedPath(), "/"))
	if segment == "." || segment == "/" || segment == "" {
		return "", errors.New("cannot derive output filename from issue URL")
	}
	name, err := url.PathUnescape(segment)
	if err != nil || name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return "", errors.New("cannot derive safe output filename from issue URL")
	}
	return name + ".epub", nil
}

func refuseExistingOutput(outputPath string) error {
	_, err := os.Lstat(outputPath)
	switch {
	case err == nil:
		return fmt.Errorf("output %q already exists", outputPath)
	case errors.Is(err, os.ErrNotExist):
		return nil
	default:
		return fmt.Errorf("check output %q: %w", outputPath, err)
	}
}

func writeEPUB(issue Issue, articles []Article, fetcher *Fetcher, outputPath string, stdout io.Writer) (resultErr error) {
	absoluteOutput, err := filepath.Abs(outputPath)
	if err != nil {
		return fmt.Errorf("resolve output %q: %w", outputPath, err)
	}
	outputDir := filepath.Dir(absoluteOutput)
	temporary, err := os.CreateTemp(outputDir, ".horizont-epub-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary output: %w", err)
	}
	temporaryPath := temporary.Name()
	if err := temporary.Close(); err != nil {
		cleanupErr := os.Remove(temporaryPath)
		if cleanupErr != nil {
			cleanupErr = fmt.Errorf("remove temporary output: %w", cleanupErr)
		}
		return errors.Join(fmt.Errorf("close temporary output: %w", err), cleanupErr)
	}
	defer func() {
		if err := os.Remove(temporaryPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			resultErr = errors.Join(resultErr, fmt.Errorf("remove temporary output: %w", err))
		}
	}()

	if err := BuildEPUB(issue, articles, fetcher, temporaryPath); err != nil {
		return err
	}
	if err := os.Link(temporaryPath, absoluteOutput); err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("output %q already exists", outputPath)
		}
		return fmt.Errorf("publish output %q: %w", outputPath, err)
	}
	if _, err := fmt.Fprintln(stdout, outputPath); err != nil {
		return fmt.Errorf("print output path: %w", err)
	}
	return nil
}
