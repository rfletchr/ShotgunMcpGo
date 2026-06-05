package main

//go:generate go run ./cmd/fetchdocs

import (
	"context"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path"
	"sort"
	"strings"
	"sync"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	sg "github.com/rfletchr/ShotgunGo"
)

//go:embed docs
var docsFS embed.FS

//go:embed prompts/query_guide.md
var queryGuide string

// ---------------------------------------------------------------------------
// ShotGrid client — lazy, validated on first use
// ---------------------------------------------------------------------------

var (
	sgClient *sg.Client
	sgOnce   sync.Once
	sgErr    error
)

func getClient() (*sg.Client, error) {
	sgOnce.Do(func() {
		siteURL := os.Getenv("SG_SITE_URL")
		scriptName := os.Getenv("SG_SCRIPT_NAME")
		scriptKey := os.Getenv("SG_SCRIPT_KEY")
		if siteURL == "" || scriptName == "" || scriptKey == "" {
			sgErr = fmt.Errorf("missing credentials: set SG_SITE_URL, SG_SCRIPT_NAME, SG_SCRIPT_KEY")
			return
		}
		c := sg.NewClient(siteURL, scriptName, scriptKey)
		project, err := c.Find("projects",
			sg.Fields("name"),
			sg.Filter("archived", sg.Is, false),
		).One(context.Background())
		if err != nil {
			sgErr = fmt.Errorf("ShotGrid connection failed: %w", err)
			return
		}
		if project == nil {
			sgErr = fmt.Errorf("connected but no projects found — check credentials")
			return
		}
		log.Printf("ShotGrid connected")
		sgClient = c
	})
	return sgClient, sgErr
}

// ---------------------------------------------------------------------------
// Filter parsing — [[field, operator, value], ...] JSON string -> Condition
// ---------------------------------------------------------------------------

func parseFilters(raw string) (sg.Condition, error) {
	var triples [][]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &triples); err != nil {
		return nil, fmt.Errorf("filters must be a JSON array of [field, operator, value] triplets: %w", err)
	}
	conditions := make([]sg.Condition, 0, len(triples))
	for _, triple := range triples {
		if len(triple) != 3 {
			return nil, fmt.Errorf("each filter must have exactly 3 elements: [field, operator, value]")
		}
		var field, op string
		if err := json.Unmarshal(triple[0], &field); err != nil {
			return nil, fmt.Errorf("filter field must be a string: %w", err)
		}
		if err := json.Unmarshal(triple[1], &op); err != nil {
			return nil, fmt.Errorf("filter operator must be a string: %w", err)
		}
		var value any
		if err := json.Unmarshal(triple[2], &value); err != nil {
			return nil, fmt.Errorf("invalid filter value: %w", err)
		}
		conditions = append(conditions, sg.Filter(field, sg.FilterRelation(op), value))
	}
	if len(conditions) == 0 {
		return nil, nil
	}
	if len(conditions) == 1 {
		return conditions[0], nil
	}
	return sg.And(conditions...), nil
}

// ---------------------------------------------------------------------------
// Docs helpers
// ---------------------------------------------------------------------------

func docTopics() []string {
	var topics []string
	fs.WalkDir(docsFS, "docs", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".rst") {
			return err
		}
		rel := strings.TrimPrefix(p, "docs/")
		rel = strings.TrimSuffix(rel, ".rst")
		topics = append(topics, rel)
		return nil
	})
	sort.Strings(topics)
	return topics
}

func readDoc(topic string) (string, error) {
	p := path.Join("docs", topic+".rst")
	data, err := docsFS.ReadFile(p)
	if err != nil {
		return "", fmt.Errorf("topic %q not found — call sg_docs_topics to see available topics", topic)
	}
	return string(data), nil
}

// ---------------------------------------------------------------------------
// Static reference data
// ---------------------------------------------------------------------------

var operatorArgs = map[string]string{
	"is":               "field_value | null",
	"is_not":           "field_value | null",
	"less_than":        "field_value | null",
	"greater_than":     "field_value | null",
	"contains":         "field_value | null",
	"not_contains":     "field_value | null",
	"starts_with":      "string",
	"ends_with":        "string",
	"between":          "[field_value | null, field_value | null]",
	"not_between":      "[field_value | null, field_value | null]",
	"in_last":          "[int, \"HOUR\" | \"DAY\" | \"WEEK\" | \"MONTH\" | \"YEAR\"]",
	"not_in_last":      "[int, \"HOUR\" | \"DAY\" | \"WEEK\" | \"MONTH\" | \"YEAR\"]",
	"in_next":          "[int, \"HOUR\" | \"DAY\" | \"WEEK\" | \"MONTH\" | \"YEAR\"]",
	"not_in_next":      "[int, \"HOUR\" | \"DAY\" | \"WEEK\" | \"MONTH\" | \"YEAR\"]",
	"in_calendar_day":  "int  // 0=today, 1=tomorrow, -1=yesterday",
	"in_calendar_week": "int  // 0=this week, 1=next week, -1=last week",
	"in_calendar_month": "int  // 0=this month, 1=next month, -1=last month",
	"in_calendar_year": "int  // 0=this year",
	"in":               "[field_value, ...]",
	"not_in":           "[field_value, ...]",
	"type_is":          "string | null  // ShotGrid entity type",
	"type_is_not":      "string | null  // ShotGrid entity type",
	"name_contains":    "string",
	"name_not_contains": "string",
	"name_starts_with": "string",
	"name_ends_with":   "string",
	"name_is":          "string",
	"name_id":          "string",
}

var operatorsByType = map[string][]string{
	"addressing":   {"is", "is_not", "contains", "not_contains", "in", "type_is", "type_is_not", "name_contains", "name_not_contains", "name_starts_with", "name_ends_with"},
	"checkbox":     {"is", "is_not"},
	"currency":     {"is", "is_not", "less_than", "greater_than", "between", "not_between", "in", "not_in"},
	"date":         {"is", "is_not", "greater_than", "less_than", "between", "in", "not_in", "in_last", "not_in_last", "in_next", "not_in_next", "in_calendar_day", "in_calendar_week", "in_calendar_month", "in_calendar_year"},
	"date_time":    {"is", "is_not", "greater_than", "less_than", "between", "in", "not_in", "in_last", "not_in_last", "in_next", "not_in_next", "in_calendar_day", "in_calendar_week", "in_calendar_month", "in_calendar_year"},
	"duration":     {"is", "is_not", "greater_than", "less_than", "between", "in", "not_in"},
	"entity":       {"is", "is_not", "in", "not_in", "type_is", "type_is_not", "name_contains", "name_not_contains", "name_is"},
	"float":        {"is", "is_not", "greater_than", "less_than", "between", "in", "not_in"},
	"image":        {"is", "is_not"},
	"list":         {"is", "is_not", "in", "not_in"},
	"multi_entity": {"is", "is_not", "in", "not_in", "type_is", "type_is_not", "name_contains", "name_not_contains"},
	"number":       {"is", "is_not", "less_than", "greater_than", "between", "not_between", "in", "not_in"},
	"percent":      {"is", "is_not", "greater_than", "less_than", "between", "in", "not_in"},
	"status_list":  {"is", "is_not", "in", "not_in"},
	"tag_list":     {"is", "is_not", "name_contains", "name_not_contains", "name_id"},
	"text":         {"is", "is_not", "contains", "not_contains", "starts_with", "ends_with", "in", "not_in"},
	"timecode":     {"is", "is_not", "greater_than", "less_than", "between", "in", "not_in"},
}

var dataTypes = map[string]any{
	"addressing":   map[string]any{"value": "list", "description": "List of dicts: [{\"type\": \"HumanUser\" | \"Group\", \"id\": int}]"},
	"checkbox":     map[string]any{"value": "bool"},
	"color":        map[string]any{"value": "string", "example": "255,0,0 | pipeline_step"},
	"currency":     map[string]any{"value": "float | null", "range": "-9999999999999.99 to 9999999999999.99"},
	"date":         map[string]any{"value": "string | null", "format": "YYYY-MM-DD", "notes": "Year must be >= 1970"},
	"date_time":    map[string]any{"value": "datetime | null", "notes": "Stored as UTC. API auto-converts to/from client local time."},
	"duration":     map[string]any{"value": "int | null", "range": "-2147483648 to 2147483647", "description": "Length of time in minutes"},
	"entity":       map[string]any{"value": "dict | null", "structure": "{\"type\": string, \"id\": int}"},
	"float":        map[string]any{"value": "float | null", "range": "-999999999.999999 to 999999999.999999"},
	"footage":      map[string]any{"value": "string | null", "format": "FF-ff (Feet-Frames)"},
	"image":        map[string]any{"value": "string | null", "notes": "Read-only."},
	"list":         map[string]any{"value": "string | null"},
	"multi_entity": map[string]any{"value": "list", "structure": "[{\"type\": string, \"id\": int}, ...]"},
	"number":       map[string]any{"value": "int | null", "range": "-2147483648 to 2147483647"},
	"password":     map[string]any{"value": "string | null", "notes": "Returned values are replaced with ******* for security."},
	"percent":      map[string]any{"value": "int | null", "range": "-2147483648 to 2147483647"},
	"serializable": map[string]any{"value": "dict | null"},
	"status_list":  map[string]any{"value": "string | null"},
	"tag_list":     map[string]any{"value": "list"},
	"text":         map[string]any{"value": "string | null"},
	"timecode":     map[string]any{"value": "int | null", "range": "-2147483648 to 2147483647", "description": "Length of time in milliseconds"},
	"url":          map[string]any{"value": "dict | null", "structure": "{\"content_type\": string, \"link_type\": \"local\" | \"url\" | \"upload\", \"name\": string, \"url\": string}"},
}

// ---------------------------------------------------------------------------
// Tool handlers
// ---------------------------------------------------------------------------

func handleEntityTypes(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	c, err := getClient()
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	var opts []int
	if pid := req.GetInt("project_id", 0); pid != 0 {
		opts = append(opts, pid)
	}
	types, err := c.EntityTypes(ctx, opts...)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	names := make([]string, 0, len(types))
	for name := range types {
		names = append(names, name)
	}
	sort.Strings(names)
	data, _ := json.MarshalIndent(names, "", "  ")
	return mcp.NewToolResultText(string(data)), nil
}

func handleFieldNames(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	entityType, err := req.RequireString("entity_type")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	c, err := getClient()
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	var opts []int
	if pid := req.GetInt("project_id", 0); pid != 0 {
		opts = append(opts, pid)
	}
	fields, err := c.Fields(ctx, entityType, opts...)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)
	data, _ := json.MarshalIndent(names, "", "  ")
	return mcp.NewToolResultText(string(data)), nil
}

func handleSchema(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	entityType, err := req.RequireString("entity_type")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	c, err := getClient()
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	var opts []int
	if pid := req.GetInt("project_id", 0); pid != 0 {
		opts = append(opts, pid)
	}
	fields, err := c.Fields(ctx, entityType, opts...)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	// filter to requested fields if specified
	if requested, err := req.RequireStringSlice("fields"); err == nil && len(requested) > 0 {
		want := make(map[string]bool, len(requested))
		for _, f := range requested {
			want[f] = true
		}
		for name := range fields {
			if !want[name] {
				delete(fields, name)
			}
		}
	}
	result := make(map[string]any, len(fields))
	for name, f := range fields {
		entry := map[string]any{
			"type":  f.DataType,
			"label": f.Label,
		}
		if f.Description != "" {
			entry["description"] = f.Description
		}
		if len(f.ValidValues) > 0 {
			entry["valid_values"] = f.ValidValues
		}
		if len(f.ValidTypes) > 0 {
			entry["valid_types"] = f.ValidTypes
		}
		result[name] = entry
	}
	data, _ := json.MarshalIndent(result, "", "  ")
	return mcp.NewToolResultText(string(data)), nil
}

// parseOrder converts a JSON string of [[field, direction], ...] to []sg.OrderField.
func parseOrder(raw string) ([]sg.OrderField, error) {
	if raw == "" {
		return nil, nil
	}
	var pairs [][]string
	if err := json.Unmarshal([]byte(raw), &pairs); err != nil {
		return nil, fmt.Errorf("order must be a JSON array of [field, direction] pairs: %w", err)
	}
	fields := make([]sg.OrderField, 0, len(pairs))
	for _, pair := range pairs {
		if len(pair) != 2 {
			return nil, fmt.Errorf("each order entry must have exactly 2 elements: [field, direction]")
		}
		dir := sg.OrderDirection(strings.ToLower(pair[1]))
		if dir != sg.Asc && dir != sg.Desc {
			return nil, fmt.Errorf("order direction must be \"asc\" or \"desc\", got %q", pair[1])
		}
		fields = append(fields, sg.OrderField{Field: pair[0], Direction: dir})
	}
	return fields, nil
}

func handleFind(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	entityType, err := req.RequireString("entity_type")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	filtersJSON, err := req.RequireString("filters")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	fields, err := req.RequireStringSlice("fields")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	limit := req.GetInt("limit", 50)
	pageNum := req.GetInt("page", 1)
	orderJSON := req.GetString("order", "")

	condition, err := parseFilters(filtersJSON)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	orderFields, err := parseOrder(orderJSON)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	c, err := getClient()
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	opts := []sg.QueryOption{sg.Fields(fields...), sg.PageSize(limit)}
	if condition != nil {
		opts = append(opts, condition)
	}
	if len(orderFields) > 0 {
		opts = append(opts, sg.Order(orderFields...))
	}

	page, err := c.Find(entityType, opts...).Page(ctx, pageNum)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	results := make([]map[string]any, 0, len(page.Entities))
	for _, e := range page.Entities {
		var m map[string]any
		if err := e.Decode(&m); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		results = append(results, m)
	}
	data, _ := json.MarshalIndent(results, "", "  ")
	return mcp.NewToolResultText(string(data)), nil
}

func handleFindOne(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	entityType, err := req.RequireString("entity_type")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	filtersJSON, err := req.RequireString("filters")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	fields, err := req.RequireStringSlice("fields")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	orderJSON := req.GetString("order", "")

	condition, err := parseFilters(filtersJSON)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	orderFields, err := parseOrder(orderJSON)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	c, err := getClient()
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	opts := []sg.QueryOption{sg.Fields(fields...)}
	if condition != nil {
		opts = append(opts, condition)
	}
	if len(orderFields) > 0 {
		opts = append(opts, sg.Order(orderFields...))
	}

	entity, err := c.Find(entityType, opts...).One(ctx)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	if entity == nil {
		return mcp.NewToolResultText("null"), nil
	}
	var m map[string]any
	if err := entity.Decode(&m); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	data, _ := json.MarshalIndent(m, "", "  ")
	return mcp.NewToolResultText(string(data)), nil
}

func handleOperators(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	fieldType := req.GetString("field_type", "")
	if fieldType != "" {
		ops, ok := operatorsByType[fieldType]
		if !ok {
			keys := make([]string, 0, len(operatorsByType))
			for k := range operatorsByType {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			return mcp.NewToolResultError(fmt.Sprintf("unknown or unsupported field type %q — filterable types: %s", fieldType, strings.Join(keys, ", "))), nil
		}
		result := make(map[string]string, len(ops))
		for _, op := range ops {
			result[op] = operatorArgs[op]
		}
		data, _ := json.MarshalIndent(map[string]any{fieldType: result}, "", "  ")
		return mcp.NewToolResultText(string(data)), nil
	}
	result := make(map[string]map[string]string, len(operatorsByType))
	for ft, ops := range operatorsByType {
		result[ft] = make(map[string]string, len(ops))
		for _, op := range ops {
			result[ft][op] = operatorArgs[op]
		}
	}
	data, _ := json.MarshalIndent(result, "", "  ")
	return mcp.NewToolResultText(string(data)), nil
}

func handleDataTypes(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	fieldType := req.GetString("field_type", "")
	if fieldType != "" {
		info, ok := dataTypes[fieldType]
		if !ok {
			return mcp.NewToolResultError(fmt.Sprintf("unknown field type %q", fieldType)), nil
		}
		data, _ := json.MarshalIndent(map[string]any{fieldType: info}, "", "  ")
		return mcp.NewToolResultText(string(data)), nil
	}
	data, _ := json.MarshalIndent(dataTypes, "", "  ")
	return mcp.NewToolResultText(string(data)), nil
}

func handleDocsTopics(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	data, _ := json.MarshalIndent(docTopics(), "", "  ")
	return mcp.NewToolResultText(string(data)), nil
}

func handleDocs(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	topic, err := req.RequireString("topic")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	content, err := readDoc(topic)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(content), nil
}

// ---------------------------------------------------------------------------
// Server setup
// ---------------------------------------------------------------------------

func buildServer() *server.MCPServer {
	s := server.NewMCPServer("sg-mcp", "1.0.0")

	s.AddTool(mcp.NewTool("sg_initialize",
		mcp.WithDescription("Call before any other sg_* tool. Returns essential guidance on filter syntax, field naming, dot notation, ordering, and common gotchas."),
	), func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText(queryGuide), nil
	})

	s.AddTool(mcp.NewTool("sg_entity_types",
		mcp.WithDescription("List all entity types available in this ShotGrid instance."),
		mcp.WithNumber("project_id", mcp.Description("Optional project ID to get entity types in the context of a specific project.")),
	), handleEntityTypes)

	s.AddTool(mcp.NewTool("sg_field_names",
		mcp.WithDescription("List all field names for a ShotGrid entity type. Cheaper than sg_schema — use this first to discover fields, then call sg_schema for details on specific fields."),
		mcp.WithString("entity_type", mcp.Required(), mcp.Description("PascalCase entity type name, e.g. Shot, HumanUser")),
		mcp.WithNumber("project_id", mcp.Description("Optional project ID for project-specific field visibility.")),
	), handleFieldNames)

	s.AddTool(mcp.NewTool("sg_schema",
		mcp.WithDescription("Get field details (type, label, description, valid values) for a ShotGrid entity type. Pass fields to limit results to specific fields rather than returning the full schema."),
		mcp.WithString("entity_type", mcp.Required(), mcp.Description("PascalCase entity type name, e.g. Shot, HumanUser")),
		mcp.WithArray("fields", mcp.Description("Optional list of field names to return details for. Omit to return all fields."), mcp.WithStringItems()),
		mcp.WithNumber("project_id", mcp.Description("Optional project ID — required for accurate status values and project-specific field configuration.")),
	), handleSchema)

	s.AddTool(mcp.NewTool("sg_find",
		mcp.WithDescription("Query ShotGrid entities. filters is a JSON string of [[field, operator, value], ...] triplets."),
		mcp.WithString("entity_type", mcp.Required(), mcp.Description("Entity type to query, e.g. Shot, Task")),
		mcp.WithString("filters", mcp.Required(), mcp.Description(`JSON array of [field, operator, value] triplets, e.g. [["sg_status_list","is","ip"],["project.Project.name","is","MyProject"]]`)),
		mcp.WithArray("fields", mcp.Required(), mcp.Description("Fields to return"), mcp.WithStringItems()),
		mcp.WithNumber("limit", mcp.Description("Max records to return (default 50)")),
		mcp.WithNumber("page", mcp.Description("Page number, 1-indexed (default 1)")),
		mcp.WithString("order", mcp.Description(`JSON array of [field, direction] pairs, e.g. [["created_at","desc"],["code","asc"]]`)),
	), handleFind)

	s.AddTool(mcp.NewTool("sg_find_one",
		mcp.WithDescription("Fetch a single ShotGrid entity. filters is a JSON string of [[field, operator, value], ...] triplets."),
		mcp.WithString("entity_type", mcp.Required(), mcp.Description("Entity type to query")),
		mcp.WithString("filters", mcp.Required(), mcp.Description("JSON array of [field, operator, value] triplets")),
		mcp.WithArray("fields", mcp.Required(), mcp.Description("Fields to return"), mcp.WithStringItems()),
		mcp.WithString("order", mcp.Description(`JSON array of [field, direction] pairs, e.g. [["created_at","desc"]]`)),
	), handleFindOne)

	s.AddTool(mcp.NewTool("sg_operators",
		mcp.WithDescription("Return valid filter operators and argument signatures. Pass a field type to filter, or omit for all types. Types that don't support filtering: password, serializable, summary, url."),
		mcp.WithString("field_type", mcp.Description("Field type to get operators for, e.g. text, entity, date")),
	), handleOperators)

	s.AddTool(mcp.NewTool("sg_data_types",
		mcp.WithDescription("Return ShotGrid data type documentation: value types, formats, and ranges."),
		mcp.WithString("field_type", mcp.Description("Field type to get docs for, e.g. date, entity, url")),
	), handleDataTypes)

	s.AddTool(mcp.NewTool("sg_docs_topics",
		mcp.WithDescription("List all available ShotGrid API documentation topics."),
	), handleDocsTopics)

	s.AddTool(mcp.NewTool("sg_docs",
		mcp.WithDescription("Return ShotGrid API documentation for a topic. Call sg_docs_topics first to see what's available."),
		mcp.WithString("topic", mcp.Required(), mcp.Description("Topic path, e.g. reference, cookbook/usage_tips")),
	), handleDocs)

	s.AddPrompt(mcp.NewPrompt("sg_query_guide",
		mcp.WithPromptDescription("ShotGrid query guidance: filter syntax, dot notation, ordering, paging, and common gotchas."),
	), func(_ context.Context, _ mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		return mcp.NewGetPromptResult(
			"ShotGrid query guidance",
			[]mcp.PromptMessage{
				mcp.NewPromptMessage(mcp.RoleUser, mcp.NewTextContent(queryGuide)),
			},
		), nil
	})

	return s
}

// ---------------------------------------------------------------------------
// Entry point
// ---------------------------------------------------------------------------

// loadDotEnv reads a .env file from the working directory and sets any
// KEY=VALUE pairs as environment variables. Missing file is silently ignored.
func loadDotEnv() {
	data, err := os.ReadFile(".env")
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		os.Setenv(strings.TrimSpace(key), strings.TrimSpace(value))
	}
}

func main() {
	http := flag.Bool("http", false, "Run as streamable-HTTP server instead of stdio")
	addr := flag.String("addr", ":3000", "Listen address for HTTP mode")
	flag.Parse()

	loadDotEnv()

	if _, err := getClient(); err != nil {
		log.Fatalf("ShotGrid init failed: %v", err)
	}

	s := buildServer()

	if *http {
		log.Printf("Starting HTTP server on %s", *addr)
		if err := server.NewStreamableHTTPServer(s).Start(*addr); err != nil {
			log.Fatal(err)
		}
		return
	}

	if err := server.ServeStdio(s); err != nil {
		log.Fatal(err)
	}
}
