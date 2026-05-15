# Aeolus

The control plane for AI agents' access to tools.

Aeolus is an open-source proxy that sits between MCP clients (Claude Desktop, Cursor, custom agents) and the MCP servers they call. It aggregates upstream servers, filters which tools each agent sees, and logs every tool call for audit and observability.

> Status: **v0.2.0 — alpha.** Multi-upstream proxy with a built-in live dashboard at `http://localhost:8765` showing tool calls in real time.

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

Requires Go 1.22+ and Node 18+ (Vite builds the dashboard; `npx` launches example MCP servers).

```bash
git clone https://github.com/aeolus-labs/aeolus.git
cd aeolus
make build                              # builds the React dashboard + Go binary
cp examples/config.example.yaml aeolus.yaml
./aeolus --config aeolus.yaml
```

With the example config, Aeolus starts a live dashboard at **http://localhost:8765**. Open it in your browser to see tool calls stream in as they happen.

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

### The dashboard

Open `http://localhost:8765` (or whatever you set in the config). The dashboard shows:

- Connected upstreams as badges in the header.
- Total call count and error count.
- A live table of tool calls — time, upstream, tool name, latency, status — newest at the top, streamed via Server-Sent Events.

The dashboard is fully embedded in the `aeolus` binary; there's no separate service to run.

### Dev mode for the dashboard

If you're hacking on the React UI, run the Vite dev server for hot reload:

```bash
make dev-web                            # Vite on :5173, proxies /api/* to :8765
./aeolus --config aeolus.yaml           # in another terminal
```

Open `http://localhost:5173` instead of `:8765` to see your edits live.

## Repo layout

```
aeolus/
├── cmd/aeolus/             Go entry point
├── internal/
│   ├── mcp/                JSON-RPC + MCP types
│   ├── config/             YAML loader
│   ├── upstream/           subprocess MCP server client
│   ├── proxy/              aggregation, filtering, observer callback
│   └── dashboard/          HTTP server, SSE, embedded React build
├── dashboard/              React source (Vite + TypeScript)
├── examples/               sample configs
└── Makefile                build orchestration
```

## v0.2.0 checklist

- [x] Multi-upstream proxy with namespaced tool names
- [x] Tool filtering (allow / deny, globs)
- [x] Structured JSON logging
- [x] Tests for filter and config validation
- [x] **Embedded React dashboard with live SSE feed**
- [x] Single-binary distribution (React assets embedded via `go:embed`)
- [x] Makefile for clean build workflow

## Roadmap

- **v0.3** — telemetry forwarding to a central endpoint; multi-user dashboard
- **v0.4** — policy engine: argument redaction, approval gates
- **v0.5** — HTTP + SSE transport for non-stdio upstreams
- **v1.0** — SSO, audit log export, on-prem deployment, Homebrew tap

## License

MIT — see [LICENSE](./LICENSE).
