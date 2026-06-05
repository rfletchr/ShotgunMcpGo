// fetchdocs downloads the shotgun_api3 source and extracts RST documentation
// into the docs/ directory for embedding into the MCP server binary.
//
// Run via: go generate ./...
package main

import (
	"archive/zip"
	"bytes"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const (
	docsURL    = "https://github.com/shotgunsoftware/python-api/archive/refs/heads/master.zip"
	docsPrefix = "python-api-master/docs/"
	outputDir  = "docs"
)

func main() {
	log.Println("Downloading shotgun_api3 docs...")

	resp, err := http.Get(docsURL)
	if err != nil {
		log.Fatal(err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Fatal(err)
	}

	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		log.Fatal(err)
	}

	// Find direct subdirectories of docs/ — their same-named .rst files are
	// index pages that just link to subtopics and aren't useful on their own.
	subdirs := map[string]bool{}
	for _, f := range zr.File {
		if !strings.HasPrefix(f.Name, docsPrefix) || !f.FileInfo().IsDir() {
			continue
		}
		rel := strings.TrimSuffix(strings.TrimPrefix(f.Name, docsPrefix), "/")
		if rel != "" && !strings.Contains(rel, "/") {
			subdirs[rel] = true
		}
	}

	os.RemoveAll(outputDir)
	os.MkdirAll(outputDir, 0755)

	for _, f := range zr.File {
		if !strings.HasPrefix(f.Name, docsPrefix) || f.FileInfo().IsDir() {
			continue
		}
		if !strings.HasSuffix(f.Name, ".rst") {
			continue
		}

		rel := strings.TrimPrefix(f.Name, docsPrefix)

		// Skip index pages where a same-named subdirectory exists.
		if subdirs[strings.TrimSuffix(rel, ".rst")] {
			continue
		}

		outPath := filepath.Join(outputDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(outPath), 0755); err != nil {
			log.Fatal(err)
		}

		rc, err := f.Open()
		if err != nil {
			log.Fatal(err)
		}
		content, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			log.Fatal(err)
		}

		if err := os.WriteFile(outPath, content, 0644); err != nil {
			log.Fatal(err)
		}
		log.Printf("  %s", rel)
	}

	log.Println("Done.")
}
