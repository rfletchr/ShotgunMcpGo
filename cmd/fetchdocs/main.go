// fetchdocs builds docs.json, the documentation tree embedded in the server.
//
// It reads the page list from the published Sphinx site's search index,
// fetches each page's HTML, converts it to markdown with pandoc, splits every
// page by heading into addressable nodes (internal/doctree) and writes one JSON
// file. The .rst sources are NOT used: the API reference in them is an
// unpopulated autodoc stub.
//
// Requires pandoc on PATH and network access. Run via: go generate ./...
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rfletchr/ShotgunMcpGo/internal/doctree"
)

const (
	siteURL    = "https://developer.shotgridsoftware.com/python-api/"
	outputFile = "docs.json"
	workers    = 6
)

var client = &http.Client{Timeout: 60 * time.Second}

func get(url string) (string, error) {
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		resp, err := client.Get(url)
		if err == nil {
			data, rerr := io.ReadAll(resp.Body)
			resp.Body.Close()
			switch {
			case rerr != nil:
				err = rerr
			case resp.StatusCode != http.StatusOK:
				err = fmt.Errorf("%s: %s", url, resp.Status)
			default:
				return string(data), nil
			}
		}
		lastErr = err
		time.Sleep(time.Duration(attempt) * time.Second)
	}
	return "", lastErr
}

var docnamesRe = regexp.MustCompile(`"docnames":\s*(\[[^\]]*\])`)

// docNames lists every page of the site, from Sphinx's search index.
func docNames() ([]string, error) {
	js, err := get(siteURL + "searchindex.js")
	if err != nil {
		return nil, err
	}
	m := docnamesRe.FindStringSubmatch(js)
	if m == nil {
		return nil, fmt.Errorf("no docnames in searchindex.js")
	}
	var names []string
	if err := json.Unmarshal([]byte(m[1]), &names); err != nil {
		return nil, err
	}
	sort.Strings(names)
	return names, nil
}

// skipIndexes drops top-level pages that only hold a toctree for a same-named
// directory ("cookbook", "advanced"). Their children already list them.
func skipIndexes(names []string) []string {
	var out []string
	for _, n := range names {
		if !strings.Contains(n, "/") && hasPrefixed(names, n+"/") {
			continue
		}
		out = append(out, n)
	}
	return out
}

func hasPrefixed(names []string, prefix string) bool {
	for _, n := range names {
		if strings.HasPrefix(n, prefix) {
			return true
		}
	}
	return false
}

func main() {
	if _, err := runPandoc("<p>x</p>"); err != nil {
		log.Fatalf("pandoc is required to build the docs: %v", err)
	}

	log.Println("Reading page list from", siteURL)
	names, err := docNames()
	if err != nil {
		log.Fatal(err)
	}
	names = skipIndexes(names)

	type result struct {
		name, md string
		err      error
	}
	jobs := make(chan string)
	results := make(chan result)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for name := range jobs {
				page, err := get(siteURL + name + ".html")
				if err != nil {
					results <- result{name: name, err: err}
					continue
				}
				md, err := pageToMarkdown(page)
				results <- result{name: name, md: md, err: err}
			}
		}()
	}
	go func() {
		for _, n := range names {
			jobs <- n
		}
		close(jobs)
		wg.Wait()
		close(results)
	}()

	pages := map[string]string{}
	var failed []string
	for r := range results {
		if r.err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", r.name, r.err))
			continue
		}
		pages[r.name] = r.md
	}
	if len(failed) > 0 {
		sort.Strings(failed)
		log.Fatalf("%d page(s) failed, not writing %s:\n  %s", len(failed), outputFile, strings.Join(failed, "\n  "))
	}

	ids := make([]string, len(names))
	for i, n := range names {
		ids[i] = doctree.PageID(n)
	}
	b := doctree.NewBuilder(ids)
	for _, n := range names { // sorted, so a page precedes its sub-pages
		b.AddPage(doctree.PageID(n), pages[n])
	}
	tree := b.Tree()
	if err := tree.Validate(); err != nil {
		log.Fatal(err)
	}

	// Guard against the bug this tool exists to avoid: shipping the autodoc stub.
	if ref := tree.Nodes["reference"]; ref == nil || ref.SubtreeChars < 50000 {
		log.Fatal("reference page looks like an unpopulated stub; refusing to write docs.json")
	}

	data, err := json.Marshal(tree)
	if err != nil {
		log.Fatal(err)
	}
	os.RemoveAll("docs") // legacy embedded .rst tree
	if err := os.WriteFile(outputFile, append(data, '\n'), 0o644); err != nil {
		log.Fatal(err)
	}
	log.Printf("Wrote %s: %d pages, %d nodes, %d KB", outputFile, len(names), len(tree.Nodes), len(data)/1024)
}
