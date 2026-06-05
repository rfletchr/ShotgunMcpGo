# Shotgrid MCP Server

A [Model Context Protocol](https://modelcontextprotocol.io) server for ShotGrid (aka. Flow Production Tracking, Shotgun, Shitgord, Prince). Gives LLMs direct READ-ONLY access to your ShotGrid instance — schema discovery, live queries, filter/operator reference, and embedded API documentation.

Built on [ShotgunGo](https://github.com/rfletchr/ShotgunGo) and [mcp-go](https://github.com/mark3labs/mcp-go). Compiles to a single self-contained binary with all documentation embedded.

## Security
This service requires you to provide credentials so some due-diligence is required, I've done my best to make sure the dependencies used in this project are secure and free from tampering.

`go.sum` is committed and pins the cryptographic hash of every dependency, ensuring reproducible builds and rejecting any dependency that has been tampered with.

The docker image build process mandates that `govulncheck` is run as part of the build process, ensuring that any dependencies which are later found to have known vulnerabilities are rejected.

If you want to run this standalone please make sure you run the following before building or running:
```bash
go install golang.org/x/vuln/cmd/govulncheck@latest
govulncheck ./...
```

## Tools

| Tool | Description |
|---------------------------------------------------------------|--------------------------------------------------------------------|
| `sg_initialize` | Call before any other tool — returns query guidance and gotchas |
| `sg_entity_types(project_id?)` | List all entity types in the instance |
| `sg_field_names(entity_type, project_id?)` | List field names cheaply — use before `sg_schema` |
| `sg_schema(entity_type, fields?, project_id?)` | Field details: type, label, description, valid values, valid types |
| `sg_find(entity_type, filters, fields, limit?, page?, order?)` | Query entities |
| `sg_find_one(entity_type, filters, fields, order?)` | Fetch a single entity |
| `sg_operators(field_type?)` | Valid filter operators and argument signatures per field type |
| `sg_data_types(field_type?)` | Value types, formats, and ranges per field type |
| `sg_docs_topics()` | List available API documentation topics |
| `sg_docs(topic)` | Return API documentation for a topic |

Pass `project_id` to schema tools to get project-specific field configuration — required for accurate status values, which vary per pipeline.

## Prompt

`sg_query_guide` is registered as an MCP prompt (clients that support prompts can inject it as conversation context). Clients that don't surface prompts can use `sg_initialize` instead, which returns the same content as a tool call.

## Credentials

Credentials are read from environment variables:

| Variable | Description |
|-------------------|--------------------------------------------------------------------|
| `SG_SITE_URL` | Your ShotGrid site URL, e.g. `https://yoursite.shotgunstudio.com` |
| `SG_SCRIPT_NAME` | API script name |
| `SG_SCRIPT_KEY` | API script key |

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

# Fetch API docs so they can be embedded (one-time, or to refresh)
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

The API documentation from [python-api](https://github.com/shotgunsoftware/python-api) is embedded in the binary at build time. To refresh it:

```bash
go generate ./...
```

This runs `cmd/fetchdocs`, which downloads the repository zip, extracts the RST files into `docs/`, and filters out index pages. The `docs/` directory is committed so the binary can be built without network access.


`govulncheck` only flags vulnerabilities in code paths that are actually reachable — not every CVE in the dependency tree. Known vulnerabilities are tracked at [pkg.go.dev/vuln](https://pkg.go.dev/vuln). If you are deploying this in a production environment, running `govulncheck` before each build is recommended.

### Query guidance

Edit `prompts/query_guide.md` to update the guidance returned by `sg_initialize` and the `sg_query_guide` prompt. A rebuild is required to pick up changes since the file is embedded at compile time.

`sg_initialize` is named so that LLMs infer that its needed to connect to ShotGrid. This means they tend to call it before making any other API requests which is handy, but you can also tell them to call it if they fail to.
