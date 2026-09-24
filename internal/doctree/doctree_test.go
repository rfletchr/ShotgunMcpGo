package doctree

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

const refMD = "# API Reference\n" +
	"\n" +
	"Intro text.\n" +
	"\n" +
	"## Shotgun()\n" +
	"\n" +
	"#### `class shotgun_api3.shotgun.Shotgun(base_url: str, script_name: str | None = None)`\n" +
	"\n" +
	"The client.\n" +
	"\n" +
	"## Shotgun Methods\n" +
	"\n" +
	"Methods intro.\n" +
	"\n" +
	"### CRUD Methods\n" +
	"\n" +
	"#### `Shotgun.create(entity_type: str, data: dict) -> dict`\n" +
	"\n" +
	"Create an entity.\n" +
	"\n" +
	"```python\n" +
	"# not a heading\n" +
	"sg.create('Shot', {})\n" +
	"```\n" +
	"\n" +
	"#### `Shotgun.find(entity_type: str, filters: list) -> list`\n" +
	"\n" +
	"Find entities.\n" +
	"\n" +
	"## Filter Syntax\n" +
	"\n" +
	"## Filter Syntax\n" +
	"\n" +
	"Repeated heading.\n"

func build(t *testing.T) *Tree {
	t.Helper()
	ids := []string{"cookbook/tasks", "cookbook/tasks/split_tasks", "reference"}
	b := NewBuilder(ids)
	b.AddPage("cookbook/tasks", "# Tasks\n\nTask intro.\n\n## Split Tasks\n\nsection, not the page\n")
	b.AddPage("cookbook/tasks/split_tasks", "# Splitting\n\nBody.\n")
	b.AddPage("reference", refMD)
	tr := b.Tree()
	if err := tr.Validate(); err != nil {
		t.Fatal(err)
	}
	return tr
}

func TestSlugAndPageID(t *testing.T) {
	for in, want := range map[string]string{
		"Shotgun()":        "shotgun",
		"Shotgun Methods":  "shotgun_methods",
		"Working  -- With": "working_with",
		"!!!":              "section",
	} {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
	if got := PageID("Cookbook/Usage-Tips"); got != "cookbook/usage_tips" {
		t.Errorf("PageID = %q", got)
	}
}

func TestStructure(t *testing.T) {
	tr := build(t)

	if want := []string{"cookbook", "reference"}; !reflect.DeepEqual(tr.Roots, want) {
		t.Errorf("roots = %v, want %v", tr.Roots, want)
	}
	// Directory with no page of its own is synthetic; a page that is also a directory keeps both.
	if n := tr.Nodes["cookbook"]; n == nil || n.Title != "Cookbook" || n.Body != "" {
		t.Errorf("synthetic dir wrong: %+v", n)
	}
	// Section "Split Tasks" must not steal the sub-page's ID.
	tasks := tr.Nodes["cookbook/tasks"]
	want := []string{"cookbook/tasks/split_tasks_2", "cookbook/tasks/split_tasks"}
	if !reflect.DeepEqual(tasks.Children, want) {
		t.Errorf("tasks children = %v, want %v", tasks.Children, want)
	}
	if tasks.Title != "Tasks" || tasks.Body != "Task intro." {
		t.Errorf("tasks page: %+v", tasks)
	}

	ref := tr.Nodes["reference"]
	if ref.Title != "API Reference" || ref.Body != "Intro text." {
		t.Errorf("reference page: %+v", ref)
	}
	// document order, and duplicate headings suffixed
	wantKids := []string{"reference/shotgun", "reference/shotgun_methods", "reference/filter_syntax", "reference/filter_syntax_2"}
	if !reflect.DeepEqual(ref.Children, wantKids) {
		t.Errorf("reference children = %v, want %v", ref.Children, wantKids)
	}
	// skipped heading levels (h2 -> h4) nest under the nearest shallower one
	cls := tr.Nodes["reference/shotgun/shotgun"]
	if cls == nil || cls.Title != "shotgun_api3.shotgun.Shotgun" {
		t.Fatalf("class node: %+v", cls)
	}
	if !strings.HasPrefix(cls.Body, "`class shotgun_api3.shotgun.Shotgun(base_url") || !strings.Contains(cls.Body, "The client.") {
		t.Errorf("class body should start with the signature: %q", cls.Body)
	}
	find := tr.Nodes["reference/shotgun_methods/crud_methods/find"]
	if find == nil || find.Title != "Shotgun.find" {
		t.Fatalf("find node: %+v", find)
	}
	// '#' inside a code fence is not a heading
	create := tr.Nodes["reference/shotgun_methods/crud_methods/create"]
	if create == nil || !strings.Contains(create.Body, "# not a heading") {
		t.Errorf("fence handling: %+v", create)
	}
	if len(create.Children) != 0 {
		t.Errorf("fenced # created a child: %v", create.Children)
	}
}

func TestClassMembersNestUnderClass(t *testing.T) {
	md := "# Exceptions\n\n" +
		"#### `exception shotgun_api3.ShotgunError`\n\nBase error.\n\n" +
		"#### `add_note()`\n\nnote A\n\n" +
		"#### `with_traceback()`\n\ntb A\n\n" +
		"#### `exception shotgun_api3.Fault`\n\nA fault.\n\n" +
		"#### `add_note()`\n\nnote B\n\n" +
		"## After\n\nlater\n"
	b := NewBuilder([]string{"exc"})
	b.AddPage("exc", md)
	tr := b.Tree()
	if err := tr.Validate(); err != nil {
		t.Fatal(err)
	}
	want := []string{"exc/shotgunerror", "exc/fault", "exc/after"}
	if !reflect.DeepEqual(tr.Nodes["exc"].Children, want) {
		t.Fatalf("children = %v, want %v", tr.Nodes["exc"].Children, want)
	}
	if got := tr.Nodes["exc/shotgunerror"].Children; !reflect.DeepEqual(got, []string{"exc/shotgunerror/add_note", "exc/shotgunerror/with_traceback"}) {
		t.Errorf("ShotgunError members = %v", got)
	}
	// same member name under two classes: distinct IDs, no numeric suffix
	if n := tr.Nodes["exc/fault/add_note"]; n == nil || !strings.Contains(n.Body, "note B") {
		t.Errorf("Fault.add_note: %+v", n)
	}
}

func TestSizes(t *testing.T) {
	tr := build(t)
	sum := 0
	for _, c := range tr.Nodes["reference"].Children {
		sum += tr.Nodes[c].SubtreeChars
	}
	ref := tr.Nodes["reference"]
	if ref.SubtreeChars != ref.Chars+sum || ref.Chars == 0 {
		t.Errorf("subtree = %d, own = %d, kids = %d", ref.SubtreeChars, ref.Chars, sum)
	}
	leaf := tr.Nodes["reference/filter_syntax"]
	if leaf.SubtreeChars != leaf.Chars {
		t.Errorf("leaf sizes differ: %+v", leaf)
	}
}

func TestJSONRoundTripAndLookup(t *testing.T) {
	tr := build(t)
	data, err := json.Marshal(tr)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Load(data)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, tr) {
		t.Error("round trip changed the tree")
	}
	if _, err := Load([]byte(`{"version":99,"roots":[],"nodes":{}}`)); err == nil {
		t.Error("wrong version should be rejected")
	}
	if _, err := Load([]byte(`{"version":1,"roots":["x"],"nodes":{}}`)); err == nil {
		t.Error("dangling root should be rejected")
	}

	if n := got.Nearest("reference/shotgun_methods/typo/deeper"); n != "reference/shotgun_methods" {
		t.Errorf("Nearest = %q", n)
	}
	if n := got.Nearest("nonsense"); n != "" {
		t.Errorf("Nearest(nonsense) = %q", n)
	}
	if !reflect.DeepEqual(got.Children(""), got.Roots) {
		t.Error("Children(\"\") should be the roots")
	}
}

func TestIndexAndRender(t *testing.T) {
	tr := build(t)

	top := tr.Index("", 1)
	if !strings.Contains(top, "cookbook  Cookbook") || !strings.Contains(top, "reference  API Reference") || strings.Contains(top, "  reference/") {
		t.Errorf("depth-1 index:\n%s", top)
	}
	deep := tr.Index("reference/shotgun_methods", 2)
	if !strings.Contains(deep, "reference/shotgun_methods/crud_methods  CRUD Methods") ||
		!strings.Contains(deep, "  reference/shotgun_methods/crud_methods/find  Shotgun.find") {
		t.Errorf("depth-2 index:\n%s", deep)
	}

	out, err := tr.Render("reference/shotgun_methods", false, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Methods intro.") || !strings.Contains(out, "## Sections") || strings.Contains(out, "Find entities.") {
		t.Errorf("non-recursive render leaked descendants or dropped the index:\n%s", out)
	}

	out, err = tr.Render("reference/shotgun_methods", true, 100000)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Find entities.") || strings.Index(out, "Create an entity.") > strings.Index(out, "Find entities.") {
		t.Errorf("recursive render missing or out of order:\n%s", out)
	}

	_, err = tr.Render("reference", true, 10)
	if err == nil || !strings.Contains(err.Error(), "limit 10") || !strings.Contains(err.Error(), "reference/shotgun_methods") {
		t.Errorf("over-cap recursive should refuse and list children, got: %v", err)
	}
	if _, err := tr.Render("nope", false, 10); err == nil {
		t.Error("unknown id should error")
	}
}
