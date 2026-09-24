package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	sg "github.com/rfletchr/ShotgunGo"
)

// sg_find_csv streams every page of a query into a CSV file instead of returning
// rows to the caller, so large result sets never pass through the LLM's context.
// The file is meant to be loaded with a SQLite tool (e.g. import_csv).

const (
	defaultExportRows = 100000
	exportPageSize    = 500 // ShotGrid's maximum page size
	exportFileMode    = 0o660
)

// exportDir is where CSV files are written. In the container this is the bind
// mount; the tool only ever takes a bare filename, so it can't write elsewhere.
func exportDir() string {
	if v := os.Getenv("SG_EXPORT_DIR"); v != "" {
		return v
	}
	return "/exports"
}

// exportPath validates a bare filename and returns its path under exportDir,
// appending .csv if missing.
func exportPath(name string) (string, error) {
	if name == "" {
		return "", errors.New("filename is required")
	}
	if name != filepath.Base(name) || strings.ContainsAny(name, `/\`) || strings.HasPrefix(name, ".") {
		return "", errors.New("filename must be a plain file name (no directories, not hidden); files are written to the server's export directory")
	}
	if !strings.HasSuffix(strings.ToLower(name), ".csv") {
		name += ".csv"
	}
	return filepath.Join(exportDir(), name), nil
}

// ---------------------------------------------------------------------------
// Columns
// ---------------------------------------------------------------------------

type fieldMeta struct {
	DataType   string
	ValidTypes []string
}

type colKind int

const (
	kindScalar colKind = iota
	kindEntity
	kindMulti
)

// csvColumn is one requested field and the CSV columns it expands to:
//
//	scalar: field
//	entity: field (name), field.id [, field.type]
//	multi:  field (names joined by ;), field.ids [, field.types]
//
// The .type column only appears when the field can link to more than one entity type.
type csvColumn struct {
	field    string
	kind     colKind
	withType bool
}

func buildColumns(fields []string, schema map[string]fieldMeta) []csvColumn {
	cols := make([]csvColumn, 0, len(fields))
	seen := map[string]bool{"id": true} // id is always the first column
	for _, f := range fields {
		if seen[f] {
			continue
		}
		seen[f] = true
		c := csvColumn{field: f}
		if m, ok := schema[f]; ok {
			switch m.DataType {
			case "entity":
				c.kind = kindEntity
			case "multi_entity":
				c.kind = kindMulti
			}
			c.withType = c.kind != kindScalar && len(m.ValidTypes) != 1
		}
		cols = append(cols, c)
	}
	return cols
}

func csvHeader(cols []csvColumn) []string {
	h := []string{"id"}
	for _, c := range cols {
		h = append(h, c.field)
		switch c.kind {
		case kindEntity:
			h = append(h, c.field+".id")
			if c.withType {
				h = append(h, c.field+".type")
			}
		case kindMulti:
			h = append(h, c.field+".ids")
			if c.withType {
				h = append(h, c.field+".types")
			}
		}
	}
	return h
}

func csvRecord(cols []csvColumn, flat map[string]any) []string {
	rec := []string{scalarCell(flat["id"])}
	for _, c := range cols {
		rec = append(rec, c.cells(flat[c.field])...)
	}
	return rec
}

func (c csvColumn) cells(v any) []string {
	switch c.kind {
	case kindEntity:
		out := make([]string, 2)
		if c.withType {
			out = make([]string, 3)
		}
		if m, ok := v.(map[string]any); ok {
			out[0], out[1] = scalarCell(m["name"]), scalarCell(m["id"])
			if c.withType {
				out[2] = scalarCell(m["type"])
			}
		} else if v != nil {
			out[0] = scalarCell(v)
		}
		return out
	case kindMulti:
		var names, ids, types []string
		if list, ok := v.([]any); ok {
			for _, item := range list {
				m, _ := item.(map[string]any)
				names = append(names, scalarCell(m["name"]))
				ids = append(ids, scalarCell(m["id"]))
				types = append(types, scalarCell(m["type"]))
			}
		} else if v != nil {
			names = []string{scalarCell(v)}
		}
		out := []string{strings.Join(names, ";"), strings.Join(ids, ";")}
		if c.withType {
			out = append(out, strings.Join(types, ";"))
		}
		return out
	}
	return []string{scalarCell(v)}
}

// scalarCell renders a JSON value as one CSV cell. Lists of scalars are joined
// with ";"; anything else structured (e.g. a link returned for a dot-notation
// field) is kept as JSON text rather than dropped.
func scalarCell(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		return strconv.FormatBool(x)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case []any:
		parts := make([]string, len(x))
		for i, item := range x {
			switch item.(type) {
			case map[string]any, []any:
				b, _ := json.Marshal(v)
				return string(b)
			}
			parts[i] = scalarCell(item)
		}
		return strings.Join(parts, ";")
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}

// ---------------------------------------------------------------------------
// Handler
// ---------------------------------------------------------------------------

// commitExport moves the finished temp file into place. Without overwrite it
// fails if dst appeared in the meantime (hard link is atomic and refuses to
// replace an existing file).
func commitExport(tmp, dst string, overwrite bool) error {
	if overwrite {
		return os.Rename(tmp, dst)
	}
	if err := os.Link(tmp, dst); err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%s already exists (pass overwrite=true to replace it)", filepath.Base(dst))
		}
		return err
	}
	return os.Remove(tmp)
}

func handleFindCSV(args json.RawMessage) (string, bool) {
	var p struct {
		EntityType string   `json:"entity_type"`
		Filters    string   `json:"filters"`
		Fields     []string `json:"fields"`
		Order      string   `json:"order"`
		Filename   string   `json:"filename"`
		Overwrite  bool     `json:"overwrite"`
		MaxRows    int      `json:"max_rows"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "invalid arguments: " + err.Error(), true
	}
	if p.EntityType == "" {
		return "entity_type is required", true
	}
	if p.Filters == "" {
		return "filters is required", true
	}
	if len(p.Fields) == 0 {
		return "fields is required", true
	}
	if p.MaxRows < 0 {
		return "max_rows must be > 0", true
	}
	if p.MaxRows == 0 {
		p.MaxRows = defaultExportRows
	}
	dst, err := exportPath(p.Filename)
	if err != nil {
		return err.Error(), true
	}
	dir := filepath.Dir(dst)
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return fmt.Sprintf("export directory %s is not available; mount a host directory there", dir), true
	}
	if !p.Overwrite {
		if _, err := os.Stat(dst); err == nil {
			return fmt.Sprintf("%s already exists (pass overwrite=true to replace it)", filepath.Base(dst)), true
		}
	}

	condition, err := parseFilters(p.Filters)
	if err != nil {
		return err.Error(), true
	}
	orderFields, err := parseOrder(p.Order)
	if err != nil {
		return err.Error(), true
	}
	c, err := getClient()
	if err != nil {
		return err.Error(), true
	}
	ctx := context.Background()

	schema, err := c.Fields(ctx, p.EntityType)
	if err != nil {
		return err.Error(), true
	}
	meta := make(map[string]fieldMeta, len(schema))
	for name, f := range schema {
		meta[name] = fieldMeta{DataType: f.DataType, ValidTypes: f.ValidTypes}
	}
	cols := buildColumns(p.Fields, meta)
	header := csvHeader(cols)

	tmp, err := os.CreateTemp(filepath.Dir(dst), ".sgexport-*.tmp")
	if err != nil {
		return err.Error(), true
	}
	defer os.Remove(tmp.Name()) // no-op once committed
	defer tmp.Close()

	w := csv.NewWriter(tmp)
	if err := w.Write(header); err != nil {
		return err.Error(), true
	}

	opts := []sg.QueryOption{sg.Fields(p.Fields...), sg.PageSize(exportPageSize)}
	if condition != nil {
		opts = append(opts, condition)
	}
	if len(orderFields) > 0 {
		opts = append(opts, sg.Order(orderFields...))
	}

	rows, truncated := 0, false
	for e, err := range c.Find(p.EntityType, opts...).Iter(ctx) {
		if err != nil {
			return fmt.Sprintf("failed after %d rows, no file written: %v", rows, err), true
		}
		if rows == p.MaxRows {
			truncated = true
			break
		}
		var m map[string]any
		if err := e.Decode(&m); err != nil {
			return err.Error(), true
		}
		if err := w.Write(csvRecord(cols, flattenEntity(m))); err != nil {
			return err.Error(), true
		}
		rows++
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return err.Error(), true
	}
	if err := tmp.Chmod(exportFileMode); err != nil {
		return err.Error(), true
	}
	if err := tmp.Close(); err != nil {
		return err.Error(), true
	}
	if err := commitExport(tmp.Name(), dst, p.Overwrite); err != nil {
		return err.Error(), true
	}

	out := map[string]any{
		"path":      dst,
		"rows":      rows,
		"columns":   header,
		"truncated": truncated,
	}
	if host := os.Getenv("SG_EXPORT_HOST_DIR"); host != "" {
		out["host_path"] = filepath.Join(host, filepath.Base(dst))
	}
	data, _ := json.MarshalIndent(out, "", "  ")
	return string(data), false
}
