// Package doctree holds the documentation as a flat map of nodes keyed by
// absolute path ID (e.g. "reference/shotgun_methods/crud_methods/find").
//
// cmd/fetchdocs builds the tree at generate time (Builder) and writes it as
// docs.json; the server embeds that file and only ever does key lookups.
//
// IDs are slugs ([a-z0-9_]) joined by "/", so a "/" can never occur inside a
// name and an ID is unambiguous. A node's parent is its ID minus the last
// segment. Children lists carry document order, which the IDs alone cannot.
package doctree

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Version is bumped when the JSON layout changes incompatibly.
const Version = 1

// Node is one documentation section.
type Node struct {
	Title    string   `json:"title"`
	Children []string `json:"children"` // direct children, in document order
	Body     string   `json:"body"`     // this section's own text only (no descendants)

	Chars        int `json:"chars"`         // len of Body, in characters
	SubtreeChars int `json:"subtree_chars"` // Chars of this node plus all descendants
}

// Tree is the whole documentation set.
type Tree struct {
	Version int              `json:"version"`
	Roots   []string         `json:"roots"` // top-level IDs, in order
	Nodes   map[string]*Node `json:"nodes"`
}

// Load parses and validates a docs.json blob.
func Load(data []byte) (*Tree, error) {
	var t Tree
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, err
	}
	if t.Version != Version {
		return nil, fmt.Errorf("docs.json version %d, want %d", t.Version, Version)
	}
	for _, r := range t.Roots {
		if t.Nodes[r] == nil {
			return nil, fmt.Errorf("root %q has no node", r)
		}
	}
	for id, n := range t.Nodes {
		for _, c := range n.Children {
			if t.Nodes[c] == nil {
				return nil, fmt.Errorf("%q lists missing child %q", id, c)
			}
		}
	}
	return &t, nil
}

// Get returns the node for an exact ID.
func (t *Tree) Get(id string) (*Node, bool) {
	n, ok := t.Nodes[id]
	return n, ok
}

// Children returns the IDs directly under id; the empty ID means the roots.
func (t *Tree) Children(id string) []string {
	if id == "" {
		return t.Roots
	}
	if n := t.Nodes[id]; n != nil {
		return n.Children
	}
	return nil
}

// Nearest returns the longest existing ancestor of id ("" if none), for
// recovering from a mistyped ID.
func (t *Tree) Nearest(id string) string {
	for id != "" {
		if _, ok := t.Nodes[id]; ok {
			return id
		}
		i := strings.LastIndex(id, "/")
		if i < 0 {
			return ""
		}
		id = id[:i]
	}
	return ""
}

// Index lists the descendants of id (roots when empty) down to depth levels,
// one per line: "<id>  <title>  (<own> chars, <total> with children)".
func (t *Tree) Index(id string, depth int) string {
	var sb strings.Builder
	t.index(&sb, t.Children(id), depth, 0)
	return strings.TrimRight(sb.String(), "\n")
}

func (t *Tree) index(sb *strings.Builder, ids []string, depth, level int) {
	if depth < 1 {
		return
	}
	for _, id := range ids {
		n := t.Nodes[id]
		fmt.Fprintf(sb, "%s%s  %s  (%d chars", strings.Repeat("  ", level), id, n.Title, n.Chars)
		if n.SubtreeChars != n.Chars {
			fmt.Fprintf(sb, ", %d with children", n.SubtreeChars)
		}
		sb.WriteString(")\n")
		t.index(sb, n.Children, depth-1, level+1)
	}
}

// Render returns the node's own text plus an index of its children. With
// recursive, the bodies of every descendant follow in document order; that is
// refused (with the sizes) if the subtree exceeds maxChars.
func (t *Tree) Render(id string, recursive bool, maxChars int) (string, error) {
	n, ok := t.Nodes[id]
	if !ok {
		return "", fmt.Errorf("no doc with id %q", id)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "# %s\n(id: %s)\n\n", n.Title, id)
	if n.Body != "" {
		sb.WriteString(n.Body + "\n\n")
	}
	if !recursive {
		if len(n.Children) > 0 {
			sb.WriteString("## Sections\n" + t.Index(id, 1) + "\n")
		}
		return strings.TrimRight(sb.String(), "\n") + "\n", nil
	}
	if n.SubtreeChars > maxChars {
		return "", fmt.Errorf("%q with all children is %d chars (limit %d); drill into a child instead:\n%s",
			id, n.SubtreeChars, maxChars, t.Index(id, 1))
	}
	var walk func(ids []string)
	walk = func(ids []string) {
		for _, cid := range ids {
			c := t.Nodes[cid]
			fmt.Fprintf(&sb, "---\n\n## %s\n(id: %s)\n\n", c.Title, cid)
			if c.Body != "" {
				sb.WriteString(c.Body + "\n\n")
			}
			walk(c.Children)
		}
	}
	walk(n.Children)
	return strings.TrimRight(sb.String(), "\n") + "\n", nil
}
