package main

import (
	"os/exec"
	"strings"
	"testing"
)

const sigHTML = `<div itemprop="articleBody">
<h3>CRUD Methods<a class="headerlink" href="#crud" title="Permalink to this heading">¶</a></h3>
<dl class="py method"><dt class="sig sig-object py" id="shotgun_api3.shotgun.Shotgun.find">
<span class="sig-prename descclassname"><span class="pre">Shotgun.</span></span><span class="sig-name descname"><span class="pre">find</span></span><span class="sig-paren">(</span><em class="sig-param"><span class="n"><span class="pre">entity_type</span></span><span class="p"><span class="pre">:</span></span><span class="w"> </span><span class="n"><span class="pre">str</span></span></em><span class="sig-paren">)</span> <span class="sig-return"><span class="sig-return-icon">&#x2192;</span> <span class="sig-return-typehint"><span class="pre">list</span></span></span><a class="reference internal" href="_modules/shotgun_api3/shotgun.html#Shotgun.find"><span class="viewcode-link"><span class="pre">[source]</span></span></a><a class="headerlink" href="#shotgun_api3.shotgun.Shotgun.find" title="Permalink to this definition">¶</a></dt>
<dd><p>Find entities.</p><div class="admonition note"><p class="admonition-title">Note</p><p>Careful.</p></div></dd></dl>
<footer>ignored</footer>`

func TestExtractAndCleanHTML(t *testing.T) {
	body, err := extractBody("<html>nav</html>" + sigHTML + "</html>")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, "footer") || strings.Contains(body, "nav") {
		t.Errorf("body not cut to the article: %q", body)
	}
	got := cleanHTML(body)
	for _, bad := range []string{"headerlink", "viewcode", "[source]", "sig-prename", "¶"} {
		if strings.Contains(got, bad) {
			t.Errorf("cleanHTML left %q in:\n%s", bad, got)
		}
	}
	want := "<h4><code>Shotgun.find(entity_type: str) -&gt; list</code></h4>"
	if !strings.Contains(got, want) {
		t.Errorf("signature not flattened.\n got: %s\nwant contains: %s", got, want)
	}
	if !strings.Contains(got, "<p><strong>Note</strong></p>") {
		t.Errorf("admonition title not bolded:\n%s", got)
	}
	if _, err := extractBody("<html>no theme</html>"); err == nil {
		t.Error("a page without the article container should be an error")
	}
}

func TestCleanMarkdown(t *testing.T) {
	in := "Parameters\n\n:\n\n-   **entity_type** ([*str*](https://docs.python.org/3.9/library/stdtypes.html#str \"(in Python v3.9)\")) – type.\n\n\n\n" +
		"See [Filter Syntax](#filter-syntax \"x\"), [other page](filter_syntax.html#a) and [site](https://example.com/x \"Title\").\n\n![diagram](_images/d.png)\n"
	got := cleanMarkdown(in)
	for _, want := range []string{
		"**Parameters**\n\n-   **entity_type** (*str*) – type.",
		"See Filter Syntax, other page and [site](https://example.com/x).",
		"\n\ndiagram\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	// pandoc 3.x form
	got3 := cleanMarkdown("Parameters:  \n- **a** (*int*) – x\n\nReturns:  \n- y\n\nA sentence ending in Parameters: not a label.\n")
	for _, want := range []string{"**Parameters**\n\n- **a**", "**Returns**\n\n- y", "ending in Parameters: not a label."} {
		if !strings.Contains(got3, want) {
			t.Errorf("pandoc 3 label form: missing %q in:\n%s", want, got3)
		}
	}
	if strings.Contains(got, "\n\n\n") {
		t.Error("blank-line runs not collapsed")
	}
}

func TestPageToMarkdown(t *testing.T) {
	if _, err := exec.LookPath("pandoc"); err != nil {
		t.Skip("pandoc not installed")
	}
	md, err := pageToMarkdown("<html>" + sigHTML)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(md, "#### `Shotgun.find(entity_type: str) -> list`") {
		t.Errorf("signature heading missing:\n%s", md)
	}
	if !strings.Contains(md, "### CRUD Methods\n") || strings.Contains(md, "¶") {
		t.Errorf("section heading wrong:\n%s", md)
	}
	if !strings.Contains(md, "Find entities.") {
		t.Errorf("body missing:\n%s", md)
	}
}
