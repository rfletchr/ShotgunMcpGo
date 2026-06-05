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
	"net/http"
	"os"
	"path"
	"sort"
	"strings"
	"sync"

	minimcp "github.com/rfletchr/MiniMCP"
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

var defaultSearchEntityTypes = []string{
	"Project", "Episode", "Sequence", "Shot", "Asset", "Version", "Task",
}

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

type summarizeTypeInfo struct {
	SummaryTypes  []string `json:"summary_types"`
	GroupingTypes []string `json:"grouping_types"`
}

var summarizeTypes = map[string]summarizeTypeInfo{
	"addressing":   {SummaryTypes: []string{"record_count", "count"}, GroupingTypes: []string{"exact", "entitytype"}},
	"checkbox":     {SummaryTypes: []string{"record_count", "count", "checked", "unchecked"}, GroupingTypes: []string{"exact"}},
	"currency":     {SummaryTypes: []string{"record_count", "count", "sum", "maximum", "minimum", "average"}, GroupingTypes: []string{"exact", "tens", "hundreds", "thousands", "tensofthousands", "hundredsofthousands", "millions"}},
	"date":         {SummaryTypes: []string{"record_count", "count", "earliest", "latest"}, GroupingTypes: []string{"exact", "day", "week", "month", "quarter", "year", "clustered_date", "oneday", "fivedays"}},
	"date_time":    {SummaryTypes: []string{"record_count", "count", "earliest", "latest"}, GroupingTypes: []string{"exact", "day", "week", "month", "quarter", "year", "clustered_date", "oneday", "fivedays"}},
	"duration":     {SummaryTypes: []string{"record_count", "count", "sum", "maximum", "minimum", "average"}, GroupingTypes: []string{"exact", "tens", "hundreds", "thousands", "tensofthousands", "hundredsofthousands", "millions"}},
	"entity":       {SummaryTypes: []string{"record_count", "count"}, GroupingTypes: []string{"exact", "entitytype"}},
	"float":        {SummaryTypes: []string{"record_count", "count", "sum", "maximum", "minimum", "average"}, GroupingTypes: []string{"exact", "tens", "hundreds", "thousands", "tensofthousands", "hundredsofthousands", "millions"}},
	"list":         {SummaryTypes: []string{"record_count", "count"}, GroupingTypes: []string{"exact", "firstletter"}},
	"multi_entity": {SummaryTypes: []string{"record_count", "count"}, GroupingTypes: []string{"exact", "entitytype"}},
	"number":       {SummaryTypes: []string{"record_count", "count", "sum", "maximum", "minimum", "average"}, GroupingTypes: []string{"exact", "tens", "hundreds", "thousands", "tensofthousands", "hundredsofthousands", "millions"}},
	"percent":      {SummaryTypes: []string{"record_count", "count", "sum", "maximum", "minimum", "average", "percentage"}, GroupingTypes: []string{"exact", "tens", "hundreds", "thousands", "tensofthousands", "hundredsofthousands", "millions"}},
	"status_list":  {SummaryTypes: []string{"record_count", "count", "status_percentage", "status_percentage_as_float", "status_list"}, GroupingTypes: []string{"exact", "firstletter"}},
	"tag_list":     {SummaryTypes: []string{"record_count", "count"}, GroupingTypes: []string{"exact"}},
	"text":         {SummaryTypes: []string{"record_count", "count"}, GroupingTypes: []string{"exact", "firstletter"}},
	"timecode":     {SummaryTypes: []string{"record_count", "count", "sum", "maximum", "minimum", "average"}, GroupingTypes: []string{"exact", "tens", "hundreds", "thousands", "tensofthousands", "hundredsofthousands", "millions"}},
}

// ---------------------------------------------------------------------------
// flattenEntity
// ---------------------------------------------------------------------------

// flattenEntity converts a REST API entity envelope into the conventional
// ShotGrid format: type, id, and all field values at the top level.
func flattenEntity(raw map[string]any) map[string]any {
	result := make(map[string]any)
	if v, ok := raw["type"]; ok {
		result["type"] = v
	}
	if v, ok := raw["id"]; ok {
		result["id"] = v
	}
	if attrs, ok := raw["attributes"].(map[string]any); ok {
		for k, v := range attrs {
			result[k] = v
		}
	}
	if rels, ok := raw["relationships"].(map[string]any); ok {
		for k, v := range rels {
			if rel, ok := v.(map[string]any); ok {
				if data, hasData := rel["data"]; hasData {
					result[k] = data
				}
			}
		}
	}
	return result
}

// ---------------------------------------------------------------------------
// Tool handlers
// ---------------------------------------------------------------------------

func handleInitialize(_ json.RawMessage) (string, bool) {
	return queryGuide, false
}

func handleEntityTypes(args json.RawMessage) (string, bool) {
	var p struct {
		ProjectID int `json:"project_id"`
	}
	json.Unmarshal(args, &p)
	c, err := getClient()
	if err != nil {
		return err.Error(), true
	}
	var opts []int
	if p.ProjectID != 0 {
		opts = append(opts, p.ProjectID)
	}
	types, err := c.EntityTypes(context.Background(), opts...)
	if err != nil {
		return err.Error(), true
	}
	names := make([]string, 0, len(types))
	for name := range types {
		names = append(names, name)
	}
	sort.Strings(names)
	data, _ := json.MarshalIndent(names, "", "  ")
	return string(data), false
}

func handleFieldNames(args json.RawMessage) (string, bool) {
	var p struct {
		EntityType string `json:"entity_type"`
		ProjectID  int    `json:"project_id"`
	}
	if err := json.Unmarshal(args, &p); err != nil || p.EntityType == "" {
		return "entity_type is required", true
	}
	c, err := getClient()
	if err != nil {
		return err.Error(), true
	}
	var opts []int
	if p.ProjectID != 0 {
		opts = append(opts, p.ProjectID)
	}
	fields, err := c.Fields(context.Background(), p.EntityType, opts...)
	if err != nil {
		return err.Error(), true
	}
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)
	data, _ := json.MarshalIndent(names, "", "  ")
	return string(data), false
}

func handleSchema(args json.RawMessage) (string, bool) {
	var p struct {
		EntityType string   `json:"entity_type"`
		Fields     []string `json:"fields"`
		ProjectID  int      `json:"project_id"`
	}
	if err := json.Unmarshal(args, &p); err != nil || p.EntityType == "" {
		return "entity_type is required", true
	}
	c, err := getClient()
	if err != nil {
		return err.Error(), true
	}
	var opts []int
	if p.ProjectID != 0 {
		opts = append(opts, p.ProjectID)
	}
	fields, err := c.Fields(context.Background(), p.EntityType, opts...)
	if err != nil {
		return err.Error(), true
	}
	if len(p.Fields) > 0 {
		want := make(map[string]bool, len(p.Fields))
		for _, f := range p.Fields {
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
	return string(data), false
}

func handleFind(args json.RawMessage) (string, bool) {
	var p struct {
		EntityType string   `json:"entity_type"`
		Filters    string   `json:"filters"`
		Fields     []string `json:"fields"`
		Limit      int      `json:"limit"`
		Page       int      `json:"page"`
		Order      string   `json:"order"`
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
	if p.Limit == 0 {
		p.Limit = 50
	}
	if p.Page == 0 {
		p.Page = 1
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
	opts := []sg.QueryOption{sg.Fields(p.Fields...), sg.PageSize(p.Limit)}
	if condition != nil {
		opts = append(opts, condition)
	}
	if len(orderFields) > 0 {
		opts = append(opts, sg.Order(orderFields...))
	}
	page, err := c.Find(p.EntityType, opts...).Page(context.Background(), p.Page)
	if err != nil {
		return err.Error(), true
	}
	results := make([]map[string]any, 0, len(page.Entities))
	for _, e := range page.Entities {
		var m map[string]any
		if err := e.Decode(&m); err != nil {
			return err.Error(), true
		}
		results = append(results, flattenEntity(m))
	}
	out := map[string]any{
		"data":     results,
		"has_next": page.HasNext(),
		"has_prev": page.HasPrev(),
	}
	data, _ := json.MarshalIndent(out, "", "  ")
	return string(data), false
}

func handleFindOne(args json.RawMessage) (string, bool) {
	var p struct {
		EntityType string   `json:"entity_type"`
		Filters    string   `json:"filters"`
		Fields     []string `json:"fields"`
		Order      string   `json:"order"`
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
	opts := []sg.QueryOption{sg.Fields(p.Fields...)}
	if condition != nil {
		opts = append(opts, condition)
	}
	if len(orderFields) > 0 {
		opts = append(opts, sg.Order(orderFields...))
	}
	entity, err := c.Find(p.EntityType, opts...).One(context.Background())
	if err != nil {
		return err.Error(), true
	}
	if entity == nil {
		return "null", false
	}
	var m map[string]any
	if err := entity.Decode(&m); err != nil {
		return err.Error(), true
	}
	data, _ := json.MarshalIndent(flattenEntity(m), "", "  ")
	return string(data), false
}

func handleTextSearch(args json.RawMessage) (string, bool) {
	var p struct {
		Text          string `json:"text"`
		EntityFilters string `json:"entity_filters"`
		Limit         int    `json:"limit"`
		Page          int    `json:"page"`
		Order         string `json:"order"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "invalid arguments: " + err.Error(), true
	}
	if p.Text == "" {
		return "text is required", true
	}
	if p.Limit == 0 {
		p.Limit = 50
	}
	if p.Page == 0 {
		p.Page = 1
	}
	c, err := getClient()
	if err != nil {
		return err.Error(), true
	}
	q := c.TextSearch(p.Text).WithPageSize(p.Limit)
	if p.EntityFilters != "" {
		var rawFilters map[string]json.RawMessage
		if err := json.Unmarshal([]byte(p.EntityFilters), &rawFilters); err != nil {
			return "entity_filters must be a JSON object mapping entity type to filter array: " + err.Error(), true
		}
		for entityType, raw := range rawFilters {
			q.FilterEntityJSON(entityType, raw)
		}
	} else {
		for _, entityType := range defaultSearchEntityTypes {
			q.FilterEntityJSON(entityType, json.RawMessage("[]"))
		}
	}
	if p.Order != "" {
		orderFields, err := parseOrder(p.Order)
		if err != nil {
			return err.Error(), true
		}
		q.WithOrder(orderFields...)
	}
	page, err := q.Page(context.Background(), p.Page)
	if err != nil {
		return err.Error(), true
	}
	results := make([]map[string]any, 0, len(page.Entities))
	for _, e := range page.Entities {
		var m map[string]any
		if err := e.Decode(&m); err != nil {
			return err.Error(), true
		}
		results = append(results, flattenEntity(m))
	}
	out := map[string]any{
		"data":     results,
		"has_next": page.HasNext(),
		"has_prev": page.HasPrev(),
	}
	data, _ := json.MarshalIndent(out, "", "  ")
	return string(data), false
}

func handleSummarize(args json.RawMessage) (string, bool) {
	var p struct {
		EntityType    string `json:"entity_type"`
		Filters       string `json:"filters"`
		SummaryFields string `json:"summary_fields"`
		Grouping      string `json:"grouping"`
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
	if p.SummaryFields == "" {
		return "summary_fields is required", true
	}
	condition, err := parseFilters(p.Filters)
	if err != nil {
		return err.Error(), true
	}
	var summaryFields []sg.SummaryField
	if err := json.Unmarshal([]byte(p.SummaryFields), &summaryFields); err != nil {
		return "summary_fields must be a JSON array of {field, type} objects: " + err.Error(), true
	}
	var grouping []sg.GroupingField
	if p.Grouping != "" {
		if err := json.Unmarshal([]byte(p.Grouping), &grouping); err != nil {
			return "grouping must be a JSON array of {field, type, direction} objects: " + err.Error(), true
		}
	}
	c, err := getClient()
	if err != nil {
		return err.Error(), true
	}
	result, err := c.Summarize(context.Background(), p.EntityType, condition, summaryFields, grouping)
	if err != nil {
		return err.Error(), true
	}
	data, _ := json.MarshalIndent(result, "", "  ")
	return string(data), false
}

func handleOperators(args json.RawMessage) (string, bool) {
	var p struct {
		FieldType string `json:"field_type"`
	}
	json.Unmarshal(args, &p)
	if p.FieldType != "" {
		ops, ok := operatorsByType[p.FieldType]
		if !ok {
			keys := make([]string, 0, len(operatorsByType))
			for k := range operatorsByType {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			return fmt.Sprintf("unknown or unsupported field type %q — filterable types: %s", p.FieldType, strings.Join(keys, ", ")), true
		}
		result := make(map[string]string, len(ops))
		for _, op := range ops {
			result[op] = operatorArgs[op]
		}
		data, _ := json.MarshalIndent(map[string]any{p.FieldType: result}, "", "  ")
		return string(data), false
	}
	result := make(map[string]map[string]string, len(operatorsByType))
	for ft, ops := range operatorsByType {
		result[ft] = make(map[string]string, len(ops))
		for _, op := range ops {
			result[ft][op] = operatorArgs[op]
		}
	}
	data, _ := json.MarshalIndent(result, "", "  ")
	return string(data), false
}

func handleDataTypes(args json.RawMessage) (string, bool) {
	var p struct {
		FieldType string `json:"field_type"`
	}
	json.Unmarshal(args, &p)
	if p.FieldType != "" {
		info, ok := dataTypes[p.FieldType]
		if !ok {
			return fmt.Sprintf("unknown field type %q", p.FieldType), true
		}
		data, _ := json.MarshalIndent(map[string]any{p.FieldType: info}, "", "  ")
		return string(data), false
	}
	data, _ := json.MarshalIndent(dataTypes, "", "  ")
	return string(data), false
}

func handleSummarizeTypes(args json.RawMessage) (string, bool) {
	var p struct {
		FieldType string `json:"field_type"`
	}
	json.Unmarshal(args, &p)
	if p.FieldType != "" {
		info, ok := summarizeTypes[p.FieldType]
		if !ok {
			keys := make([]string, 0, len(summarizeTypes))
			for k := range summarizeTypes {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			return fmt.Sprintf("unknown field type %q — supported types: %s", p.FieldType, strings.Join(keys, ", ")), true
		}
		data, _ := json.MarshalIndent(map[string]any{p.FieldType: info}, "", "  ")
		return string(data), false
	}
	data, _ := json.MarshalIndent(summarizeTypes, "", "  ")
	return string(data), false
}

func handleDocsTopics(_ json.RawMessage) (string, bool) {
	data, _ := json.MarshalIndent(docTopics(), "", "  ")
	return string(data), false
}

func handleDocs(args json.RawMessage) (string, bool) {
	var p struct {
		Topic string `json:"topic"`
	}
	if err := json.Unmarshal(args, &p); err != nil || p.Topic == "" {
		return "topic is required", true
	}
	content, err := readDoc(p.Topic)
	if err != nil {
		return err.Error(), true
	}
	return content, false
}

// ---------------------------------------------------------------------------
// Server setup
// ---------------------------------------------------------------------------

func buildServer() *minimcp.Dispatcher {
	d := minimcp.NewDispatcher()
	s := minimcp.NewServer("sg-mcp", "1.0.0")

	s.AddTool(minimcp.Tool{
		Name:        "sg_initialize",
		Description: "Call before any other sg_* tool. Returns essential guidance on filter syntax, field naming, dot notation, ordering, and common gotchas.",
		InputSchema: &minimcp.Schema{Type: "object"},
	}, handleInitialize)

	s.AddTool(minimcp.Tool{
		Name:        "sg_entity_types",
		Description: "List all entity types available in this ShotGrid instance.",
		InputSchema: &minimcp.Schema{
			Type: "object",
			Properties: minimcp.Properties{
				"project_id": {Type: "number", Description: "Optional project ID to get entity types in the context of a specific project."},
			},
		},
	}, handleEntityTypes)

	s.AddTool(minimcp.Tool{
		Name:        "sg_field_names",
		Description: "List all field names for a ShotGrid entity type. Cheaper than sg_schema — use this first to discover fields, then call sg_schema for details on specific fields.",
		InputSchema: &minimcp.Schema{
			Type: "object",
			Properties: minimcp.Properties{
				"entity_type": {Type: "string", Description: "PascalCase entity type name, e.g. Shot, HumanUser"},
				"project_id":  {Type: "number", Description: "Optional project ID for project-specific field visibility."},
			},
			Required: []string{"entity_type"},
		},
	}, handleFieldNames)

	s.AddTool(minimcp.Tool{
		Name:        "sg_schema",
		Description: "Get field details (type, label, description, valid values) for a ShotGrid entity type. Pass fields to limit results to specific fields rather than returning the full schema.",
		InputSchema: &minimcp.Schema{
			Type: "object",
			Properties: minimcp.Properties{
				"entity_type": {Type: "string", Description: "PascalCase entity type name, e.g. Shot, HumanUser"},
				"fields":      {Type: "array", Description: "Optional list of field names to return details for. Omit to return all fields.", Items: &minimcp.Schema{Type: "string"}},
				"project_id":  {Type: "number", Description: "Optional project ID — required for accurate status values and project-specific field configuration."},
			},
			Required: []string{"entity_type"},
		},
	}, handleSchema)

	s.AddTool(minimcp.Tool{
		Name:        "sg_find",
		Description: "Query ShotGrid entities. filters is a JSON string of [[field, operator, value], ...] triplets.",
		InputSchema: &minimcp.Schema{
			Type: "object",
			Properties: minimcp.Properties{
				"entity_type": {Type: "string", Description: "Entity type to query, e.g. Shot, Task"},
				"filters":     {Type: "string", Description: `JSON array of [field, operator, value] triplets, e.g. [["sg_status_list","is","ip"],["project.Project.name","is","MyProject"]]`},
				"fields":      {Type: "array", Description: "Fields to return", Items: &minimcp.Schema{Type: "string"}},
				"limit":       {Type: "number", Description: "Max records to return (default 50)"},
				"page":        {Type: "number", Description: "Page number, 1-indexed (default 1)"},
				"order":       {Type: "string", Description: `JSON array of [field, direction] pairs, e.g. [["created_at","desc"],["code","asc"]]`},
			},
			Required: []string{"entity_type", "filters", "fields"},
		},
	}, handleFind)

	s.AddTool(minimcp.Tool{
		Name:        "sg_find_one",
		Description: "Fetch a single ShotGrid entity. Returns null if nothing matches.",
		InputSchema: &minimcp.Schema{
			Type: "object",
			Properties: minimcp.Properties{
				"entity_type": {Type: "string", Description: "Entity type to query"},
				"filters":     {Type: "string", Description: "JSON array of [field, operator, value] triplets"},
				"fields":      {Type: "array", Description: "Fields to return", Items: &minimcp.Schema{Type: "string"}},
				"order":       {Type: "string", Description: `JSON array of [field, direction] pairs, e.g. [["created_at","desc"]]`},
			},
			Required: []string{"entity_type", "filters", "fields"},
		},
	}, handleFindOne)

	s.AddTool(minimcp.Tool{
		Name:        "sg_text_search",
		Description: "Search for text across entity types. Returns mixed-type results with basic attributes. Use sg_find to fetch full field sets for specific entities.",
		InputSchema: &minimcp.Schema{
			Type: "object",
			Properties: minimcp.Properties{
				"text":           {Type: "string", Description: "Text to search for."},
				"entity_filters": {Type: "string", Description: `Optional JSON object mapping entity type names to filter arrays. Omit to search across Project, Episode, Sequence, Shot, Asset, Version, and Task. e.g. {"Shot": [["project.Project.id","is",421]], "Asset": []}`},
				"limit":          {Type: "number", Description: "Max records to return (default 50)"},
				"page":           {Type: "number", Description: "Page number, 1-indexed (default 1)"},
				"order":          {Type: "string", Description: `JSON array of [field, direction] pairs, e.g. [["created_at","desc"]]`},
			},
			Required: []string{"text"},
		},
	}, handleTextSearch)

	s.AddTool(minimcp.Tool{
		Name:        "sg_summarize",
		Description: "Aggregate field data for an entity type. Returns totals and optional per-group breakdowns. Use sg_summarize_types to check valid summary and grouping types for a field data type.",
		InputSchema: &minimcp.Schema{
			Type: "object",
			Properties: minimcp.Properties{
				"entity_type":    {Type: "string", Description: "Entity type to summarize, e.g. Shot, Task"},
				"filters":        {Type: "string", Description: `JSON array of [field, operator, value] triplets. Pass "[]" to include all records.`},
				"summary_fields": {Type: "string", Description: `JSON array of {field, type} objects, e.g. [{"field":"id","type":"count"},{"field":"cut_duration","type":"sum"}]`},
				"grouping":       {Type: "string", Description: `Optional JSON array of {field, type, direction} objects, e.g. [{"field":"sg_status_list","type":"exact","direction":"asc"}]`},
			},
			Required: []string{"entity_type", "filters", "summary_fields"},
		},
	}, handleSummarize)

	s.AddTool(minimcp.Tool{
		Name:        "sg_summarize_types",
		Description: "Return valid summary types and grouping types for a ShotGrid field data type. Use before building sg_summarize calls to avoid invalid type errors.",
		InputSchema: &minimcp.Schema{
			Type: "object",
			Properties: minimcp.Properties{
				"field_type": {Type: "string", Description: "Field data type to look up, e.g. number, date, status_list. Omit to return all types."},
			},
		},
	}, handleSummarizeTypes)

	s.AddTool(minimcp.Tool{
		Name:        "sg_operators",
		Description: "Return valid filter operators and argument signatures. Pass a field type to filter, or omit for all types. Types that don't support filtering: password, serializable, summary, url.",
		InputSchema: &minimcp.Schema{
			Type: "object",
			Properties: minimcp.Properties{
				"field_type": {Type: "string", Description: "Field type to get operators for, e.g. text, entity, date"},
			},
		},
	}, handleOperators)

	s.AddTool(minimcp.Tool{
		Name:        "sg_data_types",
		Description: "Return ShotGrid data type documentation: value types, formats, and ranges.",
		InputSchema: &minimcp.Schema{
			Type: "object",
			Properties: minimcp.Properties{
				"field_type": {Type: "string", Description: "Field type to get docs for, e.g. date, entity, url"},
			},
		},
	}, handleDataTypes)

	s.AddTool(minimcp.Tool{
		Name:        "sg_docs_topics",
		Description: "List all available ShotGrid API documentation topics.",
		InputSchema: &minimcp.Schema{Type: "object"},
	}, handleDocsTopics)

	s.AddTool(minimcp.Tool{
		Name:        "sg_docs",
		Description: "Return ShotGrid API documentation for a topic. Call sg_docs_topics first to see what's available.",
		InputSchema: &minimcp.Schema{
			Type: "object",
			Properties: minimcp.Properties{
				"topic": {Type: "string", Description: "Topic path, e.g. reference, cookbook/usage_tips"},
			},
			Required: []string{"topic"},
		},
	}, handleDocs)

	s.Register(d)
	return d
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
	useHTTP := flag.Bool("http", false, "Run as streamable-HTTP server instead of stdio")
	addr := flag.String("addr", ":3000", "Listen address for HTTP mode")
	flag.Parse()

	loadDotEnv()

	if _, err := getClient(); err != nil {
		log.Fatalf("ShotGrid init failed: %v", err)
	}

	d := buildServer()

	if *useHTTP {
		log.Printf("Starting HTTP server on %s", *addr)
		if err := http.ListenAndServe(*addr, minimcp.NewHTTPHandler(d)); err != nil {
			log.Fatal(err)
		}
		return
	}

	if err := minimcp.ServeStdio(d, os.Stdin, os.Stdout); err != nil {
		log.Fatal(err)
	}
}
