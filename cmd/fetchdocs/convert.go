package main

import (
	"bytes"
	"errors"
	"html"
	"os/exec"
	"regexp"
	"strings"
)

// The published Sphinx site (sphinx_rtd_theme) is the only place the API
// reference exists in full: the .rst sources are autodoc stubs whose content
// lives in Python docstrings. So each page is fetched as HTML, cleaned of theme
// and Sphinx chrome, and converted to GitHub-flavoured markdown by pandoc.

const (
	bodyStart = `<div itemprop="articleBody">`
	bodyEnd   = `<footer>`
)

// extractBody returns the article content of a Sphinx RTD-theme page.
func extractBody(page string) (string, error) {
	i := strings.Index(page, bodyStart)
	if i < 0 {
		return "", errors.New("no articleBody container; theme changed?")
	}
	body := page[i:]
	if j := strings.Index(body, bodyEnd); j >= 0 {
		body = body[:j]
	}
	return body, nil
}

var (
	reHeaderlink = regexp.MustCompile(`(?s)<a [^>]*class="headerlink"[^>]*>.*?</a>`)
	reViewcode   = regexp.MustCompile(`(?s)<a [^>]*href="(?:\.\./)*_modules/[^>]*>.*?</a>`)
	reReturnIcon = regexp.MustCompile(`(?s)<span class="sig-return-icon">.*?</span>`)
	reSigDT      = regexp.MustCompile(`(?s)<dt class="sig[^"]*"[^>]*>(.*?)</dt>`)
	reAdmonition = regexp.MustCompile(`(?s)<p class="admonition-title">(.*?)</p>`)
	reTag        = regexp.MustCompile(`<[^>]+>`)
	reSpaces     = regexp.MustCompile(`\s+`)
)

// cleanHTML removes permalink/[source] anchors and flattens each autodoc
// signature into a single <h4><code>..</code></h4>. Without the flattening
// pandoc emits every span of a signature as its own paragraph.
func cleanHTML(body string) string {
	body = reHeaderlink.ReplaceAllString(body, "")
	body = reViewcode.ReplaceAllString(body, "")
	body = reReturnIcon.ReplaceAllString(body, " -> ")
	body = reSigDT.ReplaceAllStringFunc(body, func(m string) string {
		inner := reSigDT.FindStringSubmatch(m)[1]
		sig := html.UnescapeString(reTag.ReplaceAllString(inner, ""))
		sig = strings.TrimSpace(reSpaces.ReplaceAllString(sig, " "))
		sig = strings.ReplaceAll(sig, "( ", "(")
		return "<h4><code>" + html.EscapeString(sig) + "</code></h4>"
	})
	return reAdmonition.ReplaceAllString(body, "<p><strong>$1</strong></p>")
}

// runPandoc converts an HTML fragment to GFM. Divs and spans are dropped
// (their content kept) and raw HTML is disabled so the output is plain markdown.
func runPandoc(fragment string) (string, error) {
	cmd := exec.Command("pandoc", "-f", "html-native_divs-native_spans", "-t", "gfm-raw_html", "--wrap=none")
	cmd.Stdin = strings.NewReader(fragment)
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		return "", errors.New("pandoc: " + err.Error() + ": " + strings.TrimSpace(stderr.String()))
	}
	return out.String(), nil
}

var (
	reImage = regexp.MustCompile(`!\[([^\]]*)\]\([^)]*\)`)
	reLink  = regexp.MustCompile(`\[([^\]]*)\]\(([^)\s]*)(?:\s+"[^"]*")?\)`)
	// pandoc 2.x renders a Sphinx field-list label as "Parameters\n\n:\n\n";
	// pandoc 3.x renders "Parameters:  \n". Both become "**Parameters**".
	reFieldLabel  = regexp.MustCompile(`(?m)^([A-Z][A-Za-z ]+)\n\n:\n\n`)
	reFieldLabel3 = regexp.MustCompile(`(?m)^(Parameters|Returns|Return type|Raises|Yields|Yield type|Other Parameters|Keyword Arguments|Variables|Warns):[ ]*\n`)
	reBlankRuns   = regexp.MustCompile(`\n{3,}`)
)

// cleanMarkdown drops links that are useless out of context and tidies pandoc's
// rendering of Sphinx field lists.
//
//   - Links to other pages/anchors of the site are unresolvable here, and
//     intersphinx links to docs.python.org carry only noise: keep the text.
//   - Other external links keep their URL but lose the title attribute.
//   - "Parameters" / "Returns" labels are rendered differently by pandoc 2.x
//     (a stray ":" paragraph) and 3.x ("Label:" line); both are made bold labels.
func cleanMarkdown(md string) string {
	md = reImage.ReplaceAllString(md, "$1")
	md = reLink.ReplaceAllStringFunc(md, func(m string) string {
		sub := reLink.FindStringSubmatch(m)
		text, url := sub[1], sub[2]
		external := strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://") || strings.HasPrefix(url, "mailto:")
		if !external || strings.Contains(url, "docs.python.org/") {
			return text
		}
		return "[" + text + "](" + url + ")"
	})
	md = reFieldLabel.ReplaceAllString(md, "**$1**\n\n")
	md = reFieldLabel3.ReplaceAllString(md, "**$1**\n\n")
	return reBlankRuns.ReplaceAllString(md, "\n\n")
}

// pageToMarkdown runs the whole HTML -> markdown pipeline for one page.
func pageToMarkdown(page string) (string, error) {
	body, err := extractBody(page)
	if err != nil {
		return "", err
	}
	md, err := runPandoc(cleanHTML(body))
	if err != nil {
		return "", err
	}
	return cleanMarkdown(md), nil
}
