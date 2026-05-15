# Aeolus

The control plane for AI agents' access to tools.

Aeolus is an open-source proxy that sits between MCP clients (Claude Desktop, Cursor, custom agents) and the MCP servers they call. It aggregates upstream servers, filters which tools each agent sees, and logs every tool call for audit and observability.

> Status: **v0.0.1 — alpha.** Single upstream over stdio, tool filtering, structured logs. See the quickstart below.

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

### Try the tool filter

The example config exposes every filesystem tool. To trim the toolset the model sees, edit `aeolus.yaml`:

```yaml
tools:
  allow:
    - read_*       # only read_file, read_multiple_files, ...
  deny:
    - read_media_* # but not the binary readers
```

Restart, ask Claude what tools it has, and only the allowed names will be visible. Aeolus logs a `tools_list` event with before / after counts whenever a client lists tools.

### What the logs look like

```
{"time":"...","level":"INFO","msg":"aeolus_start","version":"0.0.1-dev","upstream":"filesystem"}
{"time":"...","level":"INFO","msg":"tools_list","before":12,"after":4,"filtered_out":8}
{"time":"...","level":"INFO","msg":"tools_call","tool":"read_file","latency_ms":3,"status":"ok"}
```

## v0.0.1 checklist

- [x] Scaffold Go project
- [x] MCP protocol types and config schema
- [x] Upstream MCP client (one server, stdio transport)
- [x] Downstream MCP server (stdio transport)
- [x] Pass-through forwarding end-to-end
- [x] Structured JSON logging of tool calls
- [x] Tool filtering (allow / deny list)
- [x] README quickstart with a real MCP server

## Roadmap

- **v0.1** — HTTP + SSE transport, multiple upstreams, per-tool latency metrics
- **v0.2** — hosted dashboard (Aeolus Cloud) for telemetry and search
- **v0.3** — policy engine: deny rules, argument redaction, approval gates
- **v1.0** — production-ready, SSO, audit log export

## License

MIT — see [LICENSE](./LICENSE).
