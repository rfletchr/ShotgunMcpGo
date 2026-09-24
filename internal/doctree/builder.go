package doctree

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Builder assembles a Tree from markdown pages. Build-time only.
type Builder struct {
	nodes     map[string]*Node
	roots     []string
	synthetic map[string]bool // directory nodes with no page of their own
	reserved  map[string]bool // page IDs, so sections never steal a page's name
}

// NewBuilder takes every page ID up front so that a section heading whose slug
// equals a sub-page's name (e.g. "tasks" inside cookbook/tasks) gets the
// suffix, not the page.
func NewBuilder(pageIDs []string) *Builder {
	b := &Builder{
		nodes:     map[string]*Node{},
		synthetic: map[string]bool{},
		reserved:  map[string]bool{},
	}
	for _, id := range pageIDs {
		b.reserved[id] = true
	}
	return b
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// Slug lowercases s and reduces it to [a-z0-9_].
func Slug(s string) string {
	s = strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(s), "_"), "_")
	if s == "" {
		return "section"
	}
	return s
}

// PageID turns a docs-relative path without extension ("cookbook/tasks/split_tasks")
// into an ID by slugging each segment.
func PageID(rel string) string {
	parts := strings.Split(rel, "/")
	for i, p := range parts {
		parts[i] = Slug(p)
	}
	return strings.Join(parts, "/")
}

func parentOf(id string) string {
	if i := strings.LastIndex(id, "/"); i >= 0 {
		return id[:i]
	}
	return ""
}

func (b *Builder) attach(id string, n *Node) {
	b.nodes[id] = n
	if p := parentOf(id); p != "" {
		b.nodes[p].Children = append(b.nodes[p].Children, id)
	} else {
		b.roots = append(b.roots, id)
	}
}

// ensureDir creates synthetic directory nodes for every missing ancestor of id.
func (b *Builder) ensureDir(id string) {
	p := parentOf(id)
	if p == "" || b.nodes[p] != nil {
		return
	}
	b.ensureDir(p)
	title := strings.Title(strings.ReplaceAll(p[strings.LastIndex(p, "/")+1:], "_", " ")) //nolint:staticcheck
	b.attach(p, &Node{Title: title})
	b.synthetic[p] = true
}

var (
	headingRe = regexp.MustCompile(`^(#{1,6})[ \t]+(.*?)[ \t]*#*[ \t]*$`)
	codeSpan  = regexp.MustCompile("^`+(.+?)`+$")
	blankRuns = regexp.MustCompile(`\n{3,}`)
)

type frame struct {
	level   int
	id      string
	body    *strings.Builder
	isClass bool // a "class"/"exception" signature heading; later member signatures nest under it
}

// AddPage splits one markdown page into nodes. The page's first H1 becomes the
// page node's title; deeper headings nest by level under the nearest shallower one.
func (b *Builder) AddPage(id, md string) {
	var page *Node
	if n := b.nodes[id]; n != nil && b.synthetic[id] {
		page = n // a directory that also has a page of its own (e.g. cookbook/tasks)
		delete(b.synthetic, id)
	} else {
		b.ensureDir(id)
		page = &Node{Title: id[strings.LastIndex(id, "/")+1:]}
		b.attach(id, page)
	}
	delete(b.reserved, id)

	stack := []*frame{{level: 0, id: id, body: &strings.Builder{}}}
	var order []*frame
	order = append(order, stack[0])
	sawH1, inFence := false, false

	for _, line := range strings.Split(md, "\n") {
		if t := strings.TrimSpace(line); strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			inFence = !inFence
		}
		m := headingRe.FindStringSubmatch(line)
		if inFence || m == nil {
			top := stack[len(stack)-1]
			top.body.WriteString(line + "\n")
			continue
		}
		level, text := len(m[1]), m[2]
		if level == 1 && !sawH1 && len(stack) == 1 {
			sawH1 = true
			page.Title = plainTitle(text)
			continue
		}
		title, slug, sig := headingNames(text)
		isClass, isMember := classKind(sig)
		// Pop to the nearest shallower heading. Autodoc emits a class and its
		// members as same-level signature headings, so a member stays under the
		// class heading that precedes it instead of becoming its sibling.
		for {
			top := stack[len(stack)-1]
			if top.level > level || (top.level == level && !(isMember && top.isClass)) {
				stack = stack[:len(stack)-1]
				continue
			}
			break
		}
		parent := stack[len(stack)-1]
		cid := b.unique(parent.id + "/" + slug)
		child := &Node{Title: title}
		b.attach(cid, child)
		f := &frame{level: level, id: cid, body: &strings.Builder{}, isClass: isClass}
		if sig != "" {
			f.body.WriteString("`" + sig + "`\n\n")
		}
		stack = append(stack, f)
		order = append(order, f)
	}
	for _, f := range order {
		b.nodes[f.id].Body = strings.TrimSpace(blankRuns.ReplaceAllString(f.body.String(), "\n\n"))
	}
}

func (b *Builder) unique(id string) string {
	if b.nodes[id] == nil && !b.reserved[id] {
		return id
	}
	for i := 2; ; i++ {
		c := id + "_" + strconv.Itoa(i)
		if b.nodes[c] == nil && !b.reserved[c] {
			return c
		}
	}
}

// plainTitle strips inline markdown noise from a heading.
func plainTitle(s string) string {
	return strings.TrimSpace(strings.NewReplacer("`", "", `\`, "", "*", "").Replace(s))
}

var sigPrefix = regexp.MustCompile(`^(class|exception|function|method|data|attribute|property|classmethod|staticmethod)\s+`)

// classKind classifies an autodoc signature: a class/exception definition, or a
// member (any other signature). Plain headings (sig == "") are neither.
func classKind(sig string) (isClass, isMember bool) {
	if sig == "" {
		return false, false
	}
	if m := sigPrefix.FindString(sig); m != "" {
		w := strings.TrimSpace(m)
		if w == "class" || w == "exception" {
			return true, false
		}
	}
	return false, true
}

// headingNames returns a node's title, its ID slug, and (for API signature
// headings) the full signature.
//
// Sphinx autodoc headings arrive as a single code span holding the whole
// signature ("Shotgun.find(entity_type: str, ...)"). Those get the bare dotted
// name as title and its last segment as slug ("find"), and keep the signature
// as the first line of the body so the index stays short.
func headingNames(text string) (title, slug, sig string) {
	if m := codeSpan.FindStringSubmatch(text); m != nil {
		sig = m[1]
		name := sigPrefix.ReplaceAllString(sig, "")
		if i := strings.IndexAny(name, "( ="); i >= 0 {
			name = name[:i]
		}
		if name != "" {
			short := name[strings.LastIndex(name, ".")+1:]
			return name, Slug(short), sig
		}
	}
	title = plainTitle(text)
	return title, Slug(title), ""
}

// Tree finalises the build: fills in sizes and returns the tree.
func (b *Builder) Tree() *Tree {
	t := &Tree{Version: Version, Roots: b.roots, Nodes: b.nodes}
	var size func(id string) int
	size = func(id string) int {
		n := t.Nodes[id]
		n.Chars = utf8.RuneCountInString(n.Body)
		n.SubtreeChars = n.Chars
		for _, c := range n.Children {
			n.SubtreeChars += size(c)
		}
		if n.Children == nil {
			n.Children = []string{}
		}
		return n.SubtreeChars
	}
	for _, r := range t.Roots {
		size(r)
	}
	return t
}

// Validate checks structural invariants; used by the generator and tests.
func (t *Tree) Validate() error {
	seen := map[string]bool{}
	var walk func(id, parent string) error
	walk = func(id, parent string) error {
		if seen[id] {
			return fmt.Errorf("%q reachable twice", id)
		}
		seen[id] = true
		n := t.Nodes[id]
		if n == nil {
			return fmt.Errorf("missing node %q", id)
		}
		if parentOf(id) != parent {
			return fmt.Errorf("%q is listed under %q but its path says %q", id, parent, parentOf(id))
		}
		for _, c := range n.Children {
			if err := walk(c, id); err != nil {
				return err
			}
		}
		return nil
	}
	for _, r := range t.Roots {
		if err := walk(r, ""); err != nil {
			return err
		}
	}
	if len(seen) != len(t.Nodes) {
		return fmt.Errorf("%d nodes unreachable from roots", len(t.Nodes)-len(seen))
	}
	return nil
}
