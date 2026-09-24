# Shotgrid MCP Server

A [Model Context Protocol](https://modelcontextprotocol.io) server for ShotGrid (aka. Flow Production Tracking, Shotgun, Shitgord, Prince). 

Gives your LLM **accurate**, live knowledge of your ShotGrid instance providing schema, entities, filter operators, and embedded API docs, allowing it to write reliable pipeline code without hallucinating field names or entity types.


- Built on [ShotgunGo](https://github.com/rfletchr/ShotgunGo) and [MiniMcp](https://github.com/rfletchr/MiniMcp) 100% stdlib go.
- Compiles to a single self-contained binary on linux, mac, and windows.

## Security
The server binary is written in 100% stdlib go with no externally owned dependencies, so no supply chain attack risk at runtime.
The one build-time exception is [pandoc](https://pandoc.org), used by `go generate` to convert the published API docs to markdown. It is installed in the Docker builder stage only and is not part of the shipped image or binary.
The build process uses go's vulncheck system to detect any stdlib issues,
This shouldn't however be an issue as you'll be running this in a controlled environment. cant hurt.


## Tools

| Tool | Description |
|---------------------------------------------------------------|--------------------------------------------------------------------|
| `sg_initialize` | Call before any other tool — returns query guidance and gotchas |
| `sg_entity_types(project_id?)` | List all entity types in the instance |
| `sg_field_names(entity_type, project_id?)` | List field names cheaply — use before `sg_schema` |
| `sg_schema(entity_type, fields?, project_id?)` | Field details: type, label, description, valid values, valid types |
| `sg_find(entity_type, filters, fields, limit?, page?, order?)` | Query entities |
| `sg_find_one(entity_type, filters, fields, order?)` | Fetch a single entity |
| `sg_find_csv(entity_type, filters, fields, filename, order?, overwrite?, max_rows?)` | Export **all** matching entities to a CSV file; returns only the path and row count |
| `sg_operators(field_type?)` | Valid filter operators and argument signatures per field type |
| `sg_data_types(field_type?)` | Value types, formats, and ranges per field type |
| `sg_docs_topics(id?, depth?)` | Browse the API documentation tree: children of `id` (top level if omitted), with sizes |
| `sg_docs(id, recursive?)` | Return one documentation section by ID: its own text plus an index of its child sections |

Pass `project_id` to schema tools to get project-specific field configuration — required for accurate status values, which vary per pipeline.

## Exporting large result sets to CSV

`sg_find` returns every row into the LLM's context. For big pulls, or anything you want to
filter, group or join, use `sg_find_csv` instead: it pages through the whole result server-side,
writes a CSV, and returns just `{path, host_path, rows, columns, truncated}`. Load the file with a
SQLite tool (e.g. [SQLiteMCP](https://github.com/rfletchr/SqliteMCP)'s `import_csv` with
`create=true, sample_rows=-1`) and query it with SQL.

- Files are written to the server's export directory (`SG_EXPORT_DIR`, `/exports` in the container).
  `filename` is a bare name only, so the tool cannot write anywhere else. It refuses to overwrite
  unless `overwrite=true`, and the file only appears once the whole export succeeded.
- `max_rows` defaults to 100000; `truncated: true` means more rows matched.
- `id` is always the first column. Link fields are flattened so the CSV stays flat:

| Field type | Columns |
|---|---|
| scalar | `field` |
| `entity` | `field` (name), `field.id`, plus `field.type` when it can link to several entity types |
| `multi_entity` | `field` (names joined by `;`), `field.ids`, plus `field.types` when several types are possible |

- In Docker the container can't write to your filesystem, so `docker-compose.yml` bind-mounts
  `SG_EXPORT_HOST_DIR` at `/exports` and runs as `SG_EXPORT_UID:SG_EXPORT_GID`. `host_path` in the
  result is the path on the host, which is what you pass to the SQLite tool.

## Prompt

`sg_query_guide` is registered as an MCP prompt (clients that support prompts can inject it as conversation context). Clients that don't surface prompts can use `sg_initialize` instead, which returns the same content as a tool call.

## Credentials

Credentials are read from environment variables:

| Variable | Description |
|-------------------|--------------------------------------------------------------------|
| `SG_SITE_URL` | Your ShotGrid site URL, e.g. `https://yoursite.shotgunstudio.com` |
| `SG_SCRIPT_NAME` | API script name |
| `SG_SCRIPT_KEY` | API script key |
| `SG_EXPORT_HOST_DIR` | Docker: existing absolute host directory for `sg_find_csv` output, mounted at `/exports` |
| `SG_EXPORT_UID` / `SG_EXPORT_GID` | Docker: uid/gid the container runs as (default 1000), so exported files are owned by you |
| `SG_EXPORT_DIR` | Directory the server writes exports to (default `/exports`). From source, set it to a real directory |

Copy `.env.example` to `.env` and fill in your values. The server validates credentials on startup — it will refuse to start if the connection fails.

## Running

### Docker (recommended)

```bash
docker compose up -d
```

The server listens on `http://127.0.0.1:3000`.

### From source

```bash
# Install and run govulncheck (optional, but recommended)
go install golang.org/x/vuln/cmd/govulncheck@latest
govulncheck ./...

# Build the embedded API docs (needs pandoc on PATH and network access; one-time, or to refresh)
go generate ./...

# stdio transport (Claude Code CLI)
go run . 

# HTTP transport (Zed, remote clients)
go run . -http

# Override listen address
go run . -http -addr :8080
```

### Binary Files
pre-build binaries can be found on the [Releases page](https://github.com/rfletchr/ShotgunMcpGo/releases)

## Connecting

### Claude Code CLI

Add to `~/.claude/.mcp.json`:

```json
{
  "mcpServers": {
    "shotgrid": {
      "command": "sg-mcp",
      "args": []
    }
  }
}
```

### Zed (SSH remote project)

Zed must connect via URL even when the server is local. When using an SSH remote project, As of time of writing Zed spawns stdio MCP servers on the local machine rather than the remote host — the HTTP transport is required so the server runs where it has network access to ShotGrid.

Run the server on the remote machine, forward the port over SSH:

```
# ~/.ssh/config
Host your-host
    LocalForward 3000 localhost:3000
```

Then configure Zed:

```json
{
  "shotgrid": {
    "url": "http://127.0.0.1:3000/mcp"
  }
}
```

## Development

### Refreshing embedded docs

The API documentation is embedded in the binary as `docs.json`, generated (and gitignored) from the [published python-api docs](https://developer.shotgridsoftware.com/python-api/). Building it needs **network access and [pandoc](https://pandoc.org) on PATH**:

```bash
go generate ./...
```

This runs `cmd/fetchdocs`, which reads the page list from the site's Sphinx search index, fetches each page's HTML, converts it to markdown with pandoc, splits every page by heading into sections, and writes `docs.json`. The site is used rather than the repository's `.rst` files because the API reference there is an unpopulated autodoc stub; the real text lives in Python docstrings that only the built site contains. The generator fails (rather than shipping stubs) if any page fails to fetch or convert, or if the reference page comes out suspiciously small.

`docs.json` is a flat map of sections keyed by path ID. IDs are lowercase slugs joined by `/`, so a section's parent is its ID minus the last segment, and lookup is a plain key access:

```json
{
  "version": 1,
  "roots": ["advanced", "authentication", "reference", "..."],
  "nodes": {
    "reference/shotgun_methods/crud_methods/find": {
      "title": "Shotgun.find",
      "children": [],
      "body": "`Shotgun.find(entity_type: str, ...)`\n\nFind entities matching the given filters...",
      "chars": 4792,
      "subtree_chars": 4792
    }
  }
}
```

`children` is in document order (the IDs alone can't express that). `body` is the section's own text only, so nothing is stored twice; `chars` / `subtree_chars` let `sg_docs_topics` show what fetching a section will cost before the LLM commits to it. Autodoc signature headings become the bare name (`find`) with the full signature as the first line of the body, and class members nest under their class (`reference/exceptions/shotgunerror/add_note`).

`govulncheck` only flags vulnerabilities in code paths that are actually reachable — not every CVE in the dependency tree. Known vulnerabilities are tracked at [pkg.go.dev/vuln](https://pkg.go.dev/vuln). If you are deploying this in a production environment, running `govulncheck` before each build is recommended.

### Query guidance

Edit `prompts/query_guide.md` to update the guidance returned by `sg_initialize` and the `sg_query_guide` prompt. A rebuild is required to pick up changes since the file is embedded at compile time.

`sg_initialize` is named so that LLMs infer that its needed to connect to ShotGrid. This means they tend to call it before making any other API requests which is handy, but you can also tell them to call it if they fail to.
