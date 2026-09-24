package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestExportPath(t *testing.T) {
	t.Setenv("SG_EXPORT_DIR", "/exports")
	for name, want := range map[string]string{
		"shots":     "/exports/shots.csv",
		"shots.csv": "/exports/shots.csv",
		"A.CSV":     "/exports/A.CSV",
	} {
		if got, err := exportPath(name); err != nil || got != want {
			t.Errorf("%q -> %q, %v", name, got, err)
		}
	}
	for _, bad := range []string{"", "../x.csv", "a/b.csv", `a\b.csv`, ".hidden.csv", "..", "/etc/passwd"} {
		if _, err := exportPath(bad); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
}

func TestColumnsAndRecords(t *testing.T) {
	schema := map[string]fieldMeta{
		"code":        {DataType: "text"},
		"project":     {DataType: "entity", ValidTypes: []string{"Project"}},
		"entity":      {DataType: "entity", ValidTypes: []string{"Shot", "Asset"}},
		"assignees":   {DataType: "multi_entity", ValidTypes: []string{"HumanUser"}},
		"sg_elements": {DataType: "multi_entity", ValidTypes: []string{"Shot", "Asset"}},
	}
	cols := buildColumns([]string{"id", "code", "project", "entity", "assignees", "sg_elements", "code", "project.Project.name", "duration"}, schema)
	wantHeader := []string{
		"id", "code",
		"project", "project.id",
		"entity", "entity.id", "entity.type",
		"assignees", "assignees.ids",
		"sg_elements", "sg_elements.ids", "sg_elements.types",
		"project.Project.name", "duration",
	}
	if got := csvHeader(cols); !reflect.DeepEqual(got, wantHeader) {
		t.Fatalf("header\n got %v\nwant %v", got, wantHeader)
	}
	flat := map[string]any{
		"id":      float64(1234567),
		"code":    "A_010",
		"project": map[string]any{"type": "Project", "id": float64(85), "name": "TRI"},
		"entity":  map[string]any{"type": "Shot", "id": float64(9), "name": "A_010"},
		"assignees": []any{
			map[string]any{"type": "HumanUser", "id": float64(1), "name": "ann"},
			map[string]any{"type": "HumanUser", "id": float64(2), "name": "bob"},
		},
		// sg_elements missing entirely, duration null, dot field arrives as a scalar
		"duration":             nil,
		"project.Project.name": "TRI",
	}
	want := []string{"1234567", "A_010", "TRI", "85", "A_010", "9", "Shot", "ann;bob", "1;2", "", "", "", "TRI", ""}
	if got := csvRecord(cols, flat); !reflect.DeepEqual(got, want) {
		t.Errorf("record\n got %q\nwant %q", got, want)
	}
	if len(csvRecord(cols, flat)) != len(csvHeader(cols)) {
		t.Error("record width != header width")
	}
	// null entity keeps the row width
	flat["entity"] = nil
	if got := csvRecord(cols, flat); len(got) != len(wantHeader) {
		t.Errorf("null entity changed width: %d", len(got))
	}
}

func TestScalarCell(t *testing.T) {
	for _, tc := range []struct {
		in   any
		want string
	}{
		{nil, ""}, {"x", "x"}, {true, "true"}, {float64(1000000), "1000000"}, {0.5, "0.5"},
		{[]any{"a", "b"}, "a;b"},
		{[]any{map[string]any{"id": float64(1)}}, `[{"id":1}]`},
		{map[string]any{"k": "v"}, `{"k":"v"}`},
	} {
		if got := scalarCell(tc.in); got != tc.want {
			t.Errorf("%v -> %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestCommitExport(t *testing.T) {
	dir := t.TempDir()
	mk := func(name, body string) string {
		p := filepath.Join(dir, name)
		os.WriteFile(p, []byte(body), 0o644)
		return p
	}
	dst := filepath.Join(dir, "out.csv")

	if err := commitExport(mk("t1", "one"), dst, false); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "one" {
		t.Errorf("got %q", b)
	}
	err := commitExport(mk("t2", "two"), dst, false)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("expected exists error, got %v", err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "one" {
		t.Errorf("existing file was clobbered: %q", b)
	}
	if err := commitExport(mk("t3", "three"), dst, true); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "three" {
		t.Errorf("overwrite failed: %q", b)
	}
}

func TestFindCSVValidation(t *testing.T) {
	t.Setenv("SG_EXPORT_DIR", filepath.Join(t.TempDir(), "missing"))
	for name, args := range map[string]string{
		"no entity":    `{"filters":"[]","fields":["code"],"filename":"a"}`,
		"no filters":   `{"entity_type":"Shot","fields":["code"],"filename":"a"}`,
		"no fields":    `{"entity_type":"Shot","filters":"[]","filename":"a"}`,
		"no filename":  `{"entity_type":"Shot","filters":"[]","fields":["code"]}`,
		"bad filename": `{"entity_type":"Shot","filters":"[]","fields":["code"],"filename":"../a"}`,
		"neg max":      `{"entity_type":"Shot","filters":"[]","fields":["code"],"filename":"a","max_rows":-1}`,
		"missing dir":  `{"entity_type":"Shot","filters":"[]","fields":["code"],"filename":"a"}`,
	} {
		if msg, isErr := handleFindCSV([]byte(args)); !isErr {
			t.Errorf("%s: expected error, got %q", name, msg)
		}
	}
}
