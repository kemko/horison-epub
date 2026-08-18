// Package main provides the horizont-epub command.
package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
)

const maxArticlesPerIssue = 500

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
	allowPrivateNetwork := flags.Bool("allow-private-network", false, "allow downloads from private network addresses")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			if _, err := fmt.Fprintln(stderr, "usage: horizont-epub [-allow-private-network] [-o output.epub] <issue-url>"); err != nil {
				return fmt.Errorf("write help: %w", err)
			}
			return nil
		}
		return fmt.Errorf("invalid arguments: %w", err)
	}
	if flags.NArg() != 1 {
		return errors.New("usage: horizont-epub [-allow-private-network] [-o output.epub] <issue-url>")
	}

	issueURL := flags.Arg(0)
	if _, err := parseHTTPURL(issueURL); err != nil {
		return fmt.Errorf("invalid issue URL %q: %w", redactURL(issueURL), err)
	}
	outputPath, err := outputPathFor(issueURL, *output)
	if err != nil {
		return err
	}
	if err := refuseExistingOutput(outputPath); err != nil {
		return err
	}

	fetcher, err := newFetcher(*allowPrivateNetwork)
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

	articleURLs, err := uniqueArticleURLs(issue)
	if err != nil {
		return err
	}
	articles := make([]Article, 0, len(articleURLs))
	for _, articleURL := range articleURLs {
		body, finalArticleURL, err := fetcher.FetchHTML(articleURL)
		if err != nil {
			return err
		}
		article, err := ParseArticle(bytes.NewReader(body), finalArticleURL)
		if err != nil {
			return fmt.Errorf("parse article %s: %w", articleURL, err)
		}
		// Keep the issue URL as the stable lookup key; ParseArticle used the
		// redirected URL above so relative resources resolve from the final page.
		article.URL = articleURL
		articles = append(articles, article)
	}

	return writeEPUB(issue, articles, fetcher, outputPath, stdout)
}

func uniqueArticleURLs(issue Issue) ([]string, error) {
	seen := make(map[string]struct{})
	var urls []string
	materials := 0
	for _, section := range issue.Sections {
		for _, article := range section.Articles {
			materials++
			if materials > maxArticlesPerIssue {
				return nil, fmt.Errorf("issue exceeds %d-article limit", maxArticlesPerIssue)
			}
			key, _, err := canonicalURL(article.URL)
			if err != nil {
				return nil, fmt.Errorf("article %q: invalid URL: %w", article.Title, err)
			}
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			urls = append(urls, article.URL)
		}
	}
	return urls, nil
}

func outputPathFor(issueURL, requested string) (string, error) {
	if strings.TrimSpace(requested) != "" {
		return requested, nil
	}
	u, err := parseHTTPURL(issueURL)
	if err != nil {
		return "", fmt.Errorf("invalid issue URL %q: %w", redactURL(issueURL), err)
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
	outputName := filepath.Base(absoluteOutput)
	outputRoot, err := os.OpenRoot(outputDir)
	if err != nil {
		return fmt.Errorf("open output directory: %w", err)
	}
	defer func() {
		if err := outputRoot.Close(); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("close output directory: %w", err))
		}
	}()
	directory, err := outputRoot.Stat(".")
	if err != nil {
		return fmt.Errorf("stat output directory: %w", err)
	}
	if directoryModeUnsafe(runtime.GOOS, directory.Mode()) {
		return fmt.Errorf("output directory %q is writable by other users", outputDir)
	}

	temporary, temporaryName, err := createTemporaryOutput(outputRoot)
	if err != nil {
		return err
	}
	defer func() {
		if err := outputRoot.Remove(temporaryName); err != nil && !errors.Is(err, os.ErrNotExist) {
			resultErr = errors.Join(resultErr, fmt.Errorf("remove temporary output: %w", err))
		}
	}()
	temporaryClosed := false
	defer func() {
		if !temporaryClosed {
			if err := temporary.Close(); err != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("close temporary output: %w", err))
			}
		}
	}()

	if err := BuildEPUB(issue, articles, fetcher, temporary); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync temporary output: %w", err)
	}
	if err := temporary.Close(); err != nil {
		temporaryClosed = true
		return fmt.Errorf("close temporary output: %w", err)
	}
	temporaryClosed = true
	if err := outputRoot.Link(temporaryName, outputName); err != nil {
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

func directoryModeUnsafe(goos string, mode os.FileMode) bool {
	return goos != "windows" && mode.Perm()&0o022 != 0
}

func createTemporaryOutput(root *os.Root) (*os.File, string, error) {
	for range 10 {
		var suffix [16]byte
		if _, err := rand.Read(suffix[:]); err != nil {
			return nil, "", fmt.Errorf("generate temporary output name: %w", err)
		}
		name := ".horizont-epub-" + hex.EncodeToString(suffix[:]) + ".tmp"
		file, err := root.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			return file, name, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, "", fmt.Errorf("create temporary output: %w", err)
		}
	}
	return nil, "", errors.New("create temporary output: too many name collisions")
}
