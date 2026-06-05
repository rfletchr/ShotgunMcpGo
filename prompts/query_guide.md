# ShotGrid Query Guide

## Entity types

Entity types use PascalCase in all tool calls: `Shot`, `Task`, `HumanUser`, `CustomEntity01`.
Use `sg_entity_types` to discover what exists in this instance before guessing.

## Fields

Never guess field names. Use the following pattern to discover them efficiently:

1. `sg_field_names(entity_type)` — returns the full list of field names cheaply
2. `sg_schema(entity_type, fields=[...])` — returns full details for only the fields you care about

Avoid calling `sg_schema` without a `fields` filter on entity types with many fields —
the full schema for a complex entity can be very large.

## Filters

Filters are a JSON array of `[field, operator, value]` triplets:

```json
[["sg_status_list", "is", "ip"], ["project.Project.name", "is", "MyProject"]]
```

Multiple filters are combined with AND. Use `sg_operators(field_type)` to check which
operators are valid for a given field type before constructing a filter.

## Linked field dot notation

To filter or return fields on a linked entity, use dot notation:

```
entity_field.LinkedEntityType.linked_field
```

Examples:
- `project.Project.name` — the name of the linked Project
- `task_assignees.HumanUser.login` — the login of assigned users
- `sg_sequence.Sequence.code` — the code of the linked Sequence

The middle segment must be the exact PascalCase entity type, which you can confirm
with `sg_schema`.

## Ordering

Order is a JSON array of `[field, direction]` pairs where direction is `"asc"` or `"desc"`:

```json
[["created_at", "desc"], ["code", "asc"]]
```

Order affects which record `sg_find_one` returns, so always specify it explicitly when
the result depends on recency or priority.

## Paging

`sg_find` and `sg_text_search` return one page of results wrapped in `{"data": [...], "has_next": bool, "has_prev": bool}`.
Default page size is 50, default page is 1. Check `has_next` to know whether more records exist before advancing `page`.

## Text search

`sg_text_search(text, entity_filters, limit?, page?, order?)` searches across entity types by text.
`entity_filters` is **required** — the API needs at least one entity type specified.
Results contain mixed entity types with basic attributes — use `sg_find` to fetch full field sets
for the IDs you want.

`entity_filters` is a JSON object mapping entity type names to filter arrays (same triplet format as `sg_find` filters).
Use an empty array `[]` to include all records of that type with no additional filter:

```json
{"Shot": [["project.Project.id", "is", 421]], "Asset": []}
```

## Summarize

`sg_summarize(entity_type, filters, summary_fields, grouping?)` aggregates field data.

`summary_fields` is a JSON array of `{field, type}` objects:
```json
[{"field": "id", "type": "count"}, {"field": "cut_duration", "type": "sum"}]
```

Valid summary types: `record_count`, `count`, `sum`, `maximum`, `minimum`, `average`,
`earliest`, `latest`, `percentage`, `status_percentage`, `status_percentage_as_float`,
`status_list`, `checked`, `unchecked`.

- `record_count` counts matched records regardless of whether the field has a value; `count` counts non-null values for a specific field.
- Valid types depend on the field's data type — use `sg_summarize_types(field_type)` to check before building a query.

`grouping` (optional) is a JSON array of `{field, type, direction}` objects:
```json
[{"field": "sg_status_list", "type": "exact", "direction": "asc"}]
```

Valid grouping types: `exact`, `tens`, `hundreds`, `thousands`, `tensofthousands`,
`hundredsofthousands`, `millions`, `day`, `week`, `month`, `quarter`, `year`,
`clustered_date`, `oneday`, `fivedays`, `entitytype`, `firstletter`.

Multiple grouping objects produce nested buckets.

### Summarize response shape

```json
{
  "summaries": {"id": 1649},
  "groups": [
    {"group_name": "ip", "group_value": "ip", "summaries": {"id": 42}},
    {"group_name": "wtg", "group_value": "wtg", "summaries": {"id": 107}}
  ]
}
```

- `summaries` — totals across all matched records (always present).
- `groups` — one entry per bucket; only present when `grouping` is specified.
- When grouping by an entity field, `group_value` is a full entity dict rather than a plain string: `{"type": "Asset", "id": 922, "name": "PuppyA"}`.
- Nested grouping: each group entry may itself contain a `groups` array with the same structure for the next grouping level.

## Linked entity fields

When a field references another entity (e.g. `project`, `created_by`, `task_assignees`),
only `id` and `type` are returned by default:

```json
"project": {"id": 123, "type": "Project"}
```

To access additional fields on a linked entity, either:

1. Use dot notation in the `fields` list — the entity type must be included as the middle segment: `"project.Project.name"`, `"created_by.HumanUser.email"`
2. Use the returned `id` to make a separate `sg_find_one` query on that entity type

## Common gotchas

- `sg_status_list` stores short codes (`ip`, `fin`, `hld`), not display labels.
  Valid status codes are pipeline-specific and can vary per project — always call
  `sg_schema(entity_type, project_id=<id>)` to get accurate values for the project
  you are working with rather than assuming codes from another context.
- Entity references in results are `{"type": "EntityType", "id": int}` dicts — not names.
  Request the name field explicitly, e.g. `["name"]` in fields.
- `sg_find_one` returns `null` if nothing matches — not an error.
- Date values must be `"YYYY-MM-DD"` strings. Datetimes are UTC on the server;
  the API converts to/from client local time automatically.
- Fields not explicitly requested are not returned. Always specify every field you need.
