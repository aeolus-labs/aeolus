# Aeolus

The control plane for AI agents' access to tools.

Aeolus is an open-source proxy that sits between MCP clients (Claude Desktop, Cursor, custom agents) and the MCP servers they call. It aggregates upstream servers, filters which tools each agent sees, and logs every tool call for audit and observability.

> Status: **v0.1.0 — alpha.** Multiple upstreams over stdio, namespaced tool names, allow/deny filtering, structured logs.

## Why

MCP gives agents access to tools, but adoption at scale runs into three gaps:

1. **Tool bloat.** Loading every tool from every connected MCP server burns context tokens, raises API bills, and degrades model performance.
2. **No audit trail.** When an agent calls a tool, nobody logs who, what, when, or with what arguments.
3. **No policy.** Any engineer can wire any MCP server to any agent — including ones that touch production.

Aeolus is the layer that closes those gaps without changing how MCP servers or clients work.

## How it fits

Today:

```
Claude Desktop  ─►  MCP server (filesystem)
                ─►  MCP server (github)
                ─►  MCP server (postgres)
```

With Aeolus:

```
Claude Desktop  ─►  Aeolus  ─►  MCP server (filesystem)
                            ─►  MCP server (github)
                            ─►  MCP server (postgres)
```

The client sees one MCP endpoint. Aeolus handles aggregation, filtering, auth, and logging.

## Quickstart

Requires Go 1.22+ and Node (`npx`) for the example MCP server.

```bash
git clone https://github.com/aeolus-labs/aeolus.git
cd aeolus
go build -o aeolus ./cmd/aeolus
cp examples/config.example.yaml aeolus.yaml
./aeolus --config aeolus.yaml
```

The proxy speaks MCP on its own stdin/stdout, so plug it into any MCP client the same way you would the underlying server. For Claude Desktop, edit `~/Library/Application Support/Claude/claude_desktop_config.json`:

```json
{
  "mcpServers": {
    "aeolus": {
      "command": "/absolute/path/to/aeolus",
      "args": ["--config", "/absolute/path/to/aeolus.yaml"]
    }
  }
}
```

Restart Claude Desktop. The filesystem tools will appear, proxied through Aeolus. Tool calls are logged as JSON on the proxy's stderr.

### Namespaced tool names

When Aeolus aggregates tools from multiple upstreams, every tool is exposed to the client with its upstream name as a prefix:

```
filesystem  →  filesystem.read_file, filesystem.write_file, ...
github      →  github.create_issue, github.list_repos, ...
```

This avoids collisions between upstreams that share tool names, and makes filter rules read naturally — `github.delete_*` matches all destructive GitHub operations regardless of what else `github` exposes.

### Try the tool filter

```yaml
tools:
  allow:
    - filesystem.read_*    # only filesystem read operations
    - github.list_*        # and read-only GitHub queries
  deny:
    - filesystem.read_media_*
```

Restart your client. `tools/list` will return only the matching tools, and Aeolus logs the before / after count.

### What the logs look like

```
{"msg":"aeolus_start","version":"0.1.0-dev","upstreams":["filesystem","github"]}
{"msg":"upstream_initialized","name":"filesystem","server":"filesystem","protocol":"2024-11-05"}
{"msg":"tools_loaded","count":26}
{"msg":"tools_list","before":26,"after":8,"filtered_out":18}
{"msg":"tools_call","tool":"filesystem.read_file","upstream":"filesystem","latency_ms":3,"status":"ok"}
```

## v0.1.0 checklist

- [x] Scaffold Go project
- [x] MCP protocol types and config schema
- [x] Upstream MCP client (request/response API over stdio)
- [x] Downstream MCP server with `initialize` + `tools/list` + `tools/call`
- [x] **Multiple upstreams** with namespaced tool names
- [x] Structured JSON logging
- [x] Tool filtering (allow / deny, globs)
- [x] Tests for filter and config validation

## Roadmap

- **v0.1.1** — HTTP + SSE transport for upstreams that aren't stdio subprocesses
- **v0.1.2** — release binaries, Homebrew tap, `aeolus init`
- **v0.2** — embedded local dashboard (`aeolus` serves a web UI on `:8080` showing live tool calls)
- **v0.3** — telemetry forwarding + hosted Aeolus Cloud dashboard
- **v0.4** — policy engine: argument redaction, approval gates
- **v1.0** — SSO, audit log export, on-prem deployment

## License

MIT — see [LICENSE](./LICENSE).
