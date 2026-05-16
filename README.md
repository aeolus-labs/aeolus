# Aeolus

A local gateway for Model Context Protocol (MCP) servers.

Aeolus sits between any MCP client — Claude Code, Cursor, GitHub Copilot, Zed, Continue, your own agent, anything that speaks the protocol — and the MCP servers it wants to call. It aggregates upstream servers, filters which tools each client sees, keeps secrets out of plaintext configs, and ships a live dashboard for inspecting every call.

> Status: **v0.3.7 — alpha.** Single-binary, runs on your laptop.

## Why

MCP gives agents access to tools, but using it directly leaves three gaps:

1. **Tool bloat.** Loading every tool from every connected MCP server burns context tokens, raises API bills, and degrades model performance.
2. **No audit trail.** When an agent calls a tool, nobody logs who, what, when, with what arguments.
3. **No policy.** Any process on your machine can configure any MCP server — including ones that touch production.

Aeolus closes those gaps without changing how MCP servers or clients work.

## How it fits

Today, each MCP client talks to MCP servers directly:

```
Consumer  ─►  filesystem MCP
          ─►  github MCP
          ─►  postgres MCP
```

With Aeolus, the consumer talks to one endpoint and Aeolus fans out:

```
Consumer  ─►  Aeolus  ─►  filesystem MCP   (stdio)
                      ─►  github MCP       (stdio)
                      ─►  postgres MCP     (stdio)
                      ─►  example.com/mcp  (http)
```

Aeolus handles:
- **Aggregation** — many MCP servers, one endpoint
- **Namespacing** — tool names prefixed by upstream (`filesystem.read_file`) so collisions don't happen
- **Filtering** — allow/deny rules per tool, with globs
- **Secret management** — env values and HTTP headers can live in the OS keychain, never in YAML
- **Observability** — every tool call logged; live dashboard at `http://localhost:8765`
- **Hot reload** — edit `aeolus.yaml` or use the dashboard editor; changes apply without disconnecting the client

## Quickstart

Requires Go 1.22+ and Node 18+ (Node is only needed to build the dashboard; the resulting binary is self-contained).

```bash
git clone https://github.com/aeolus-labs/aeolus.git
cd aeolus
make build                                 # React dashboard + Go binary
cp examples/config.example.yaml aeolus.yaml
./aeolus --config aeolus.yaml
```

You should see `dashboard_listening url=http://127.0.0.1:8765` in stderr. Open it — that's the live UI.

To make a client use Aeolus, point it at the absolute path of the `aeolus` binary and the absolute path of `aeolus.yaml`. Snippets per client below.

## Client setup

All snippets assume `aeolus` is at `/abs/path/to/aeolus` and your config is at `/abs/path/to/aeolus.yaml`.

### Claude Desktop / Claude Code

Edit `~/Library/Application Support/Claude/claude_desktop_config.json` (Claude Code uses a per-project equivalent via `claude mcp add aeolus <path>`):

```json
{
  "mcpServers": {
    "aeolus": {
      "command": "/abs/path/to/aeolus",
      "args": ["--config", "/abs/path/to/aeolus.yaml"]
    }
  }
}
```

### Cursor

Edit `~/.cursor/mcp.json` (global) or `.cursor/mcp.json` in your project root:

```json
{
  "mcpServers": {
    "aeolus": {
      "command": "/abs/path/to/aeolus",
      "args": ["--config", "/abs/path/to/aeolus.yaml"]
    }
  }
}
```

### GitHub Copilot (VS Code)

Add to `.vscode/mcp.json` in your workspace or VS Code user settings:

```json
{
  "servers": {
    "aeolus": {
      "type": "stdio",
      "command": "/abs/path/to/aeolus",
      "args": ["--config", "/abs/path/to/aeolus.yaml"]
    }
  }
}
```

### Zed

In `~/.config/zed/settings.json`:

```json
{
  "context_servers": {
    "aeolus": {
      "command": {
        "path": "/abs/path/to/aeolus",
        "args": ["--config", "/abs/path/to/aeolus.yaml"]
      }
    }
  }
}
```

### Continue.dev

In `~/.continue/config.json`:

```json
{
  "experimental": {
    "modelContextProtocolServers": [
      {
        "transport": {
          "type": "stdio",
          "command": "/abs/path/to/aeolus",
          "args": ["--config", "/abs/path/to/aeolus.yaml"]
        }
      }
    ]
  }
}
```

### Custom agent (Anthropic / OpenAI / local model)

Aeolus is just an MCP server itself. Spawn it like any other and let your agent SDK do the rest. With the official `@modelcontextprotocol/sdk` (TS):

```ts
import { StdioClientTransport } from '@modelcontextprotocol/sdk/client/stdio.js'
import { Client } from '@modelcontextprotocol/sdk/client/index.js'

const transport = new StdioClientTransport({
  command: '/abs/path/to/aeolus',
  args: ['--config', '/abs/path/to/aeolus.yaml'],
})
const client = new Client({ name: 'my-agent', version: '0.1.0' })
await client.connect(transport)
```

## Configuration

`aeolus.yaml` describes upstreams, tool rules, logging, and dashboard settings.

```yaml
upstreams:
  - name: filesystem
    command: npx
    args: ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"]

  - name: github
    command: npx
    args: ["-y", "@modelcontextprotocol/server-github"]
    env:
      - GITHUB_PERSONAL_ACCESS_TOKEN=keychain:github.GITHUB_PERSONAL_ACCESS_TOKEN

  - name: hosted
    transport: http
    url: https://mcp.example.com/mcp
    headers:
      Authorization: keychain:hosted.Authorization

tools:
  allow:
    - filesystem.read_*    # only filesystem read operations
    - github.list_*        # and read-only GitHub queries
    - hosted.*             # everything from the hosted server
  deny:
    - filesystem.read_media_*

log:
  level: info
  format: json

dashboard:
  enabled: true
  addr: "localhost:8765"
```

### Transports

- **stdio** (default) — Aeolus spawns the MCP server as a subprocess and talks over stdin/stdout. Best for npm/pip-distributed MCP servers.
- **http** — Aeolus POSTs JSON-RPC to a Streamable HTTP endpoint per the MCP spec. Best for hosted MCP servers. Set `url:` and optional `headers:`.

### Secrets in the keychain

Any value in `env:` or `headers:` can be `keychain:<name>`. Aeolus resolves it from the OS keychain (macOS Keychain, Windows Credential Manager, libsecret on Linux) at spawn time. The actual secret never lives in `aeolus.yaml`. The dashboard's "🔓 lock" toggle stores values into the keychain on save.

### Hot reload

Edits to `aeolus.yaml` (by hand or by the dashboard) are picked up immediately — Aeolus reloads the proxy without disconnecting the client. Invalid configs are logged and ignored; the previous good config keeps running.

## The dashboard

Visit `http://localhost:8765`.

- **Live** tab — every tool call streamed in real time: time, upstream, tool name, latency, status. Filter by tool name, upstream, status. Top-N per-tool stats with p50/p95 latencies.
- **Settings** tab — connected upstreams with per-upstream Setup / Tools views; an Add / Edit / Remove flow that probes new servers before saving; a Catalog browser of ~2,600 MCP servers from the official MCP Registry, filterable by transport / auth / source.

## Tool name namespacing

Aeolus prefixes every upstream tool with the upstream name:

```
filesystem  →  filesystem.read_file, filesystem.write_file, ...
github      →  github.create_issue, github.list_repos, ...
```

Two upstreams with a tool named `read_file` don't collide. Filter rules like `github.delete_*` match all destructive GitHub operations.

## Repo layout

```
aeolus/
├── cmd/aeolus/             Go entry point
├── internal/
│   ├── mcp/                JSON-RPC + MCP types
│   ├── config/             YAML loader
│   ├── upstream/           stdio + http server impls
│   ├── proxy/              aggregation, filtering, hot reload, observer
│   ├── secrets/            OS keychain wrapper
│   ├── watcher/            aeolus.yaml file watcher
│   └── dashboard/          HTTP + SSE; registry catalog; embedded React build
├── dashboard/              React source (Vite + TypeScript)
├── examples/               sample configs
└── Makefile                build orchestration
```

## v0.3.x checklist

- [x] Multi-upstream proxy with namespaced tool names
- [x] Tool filtering (allow / deny, globs)
- [x] Embedded React dashboard with live SSE feed
- [x] Dashboard-driven config editor with hot reload
- [x] Probe-before-save flow for new upstreams
- [x] Catalog from the official MCP Registry (~2,600 entries) with search and filters
- [x] OS keychain integration for env + header secrets
- [x] HTTP transport for upstreams
- [x] File watcher for hand-edits to `aeolus.yaml`

## What's next

- Catalog quality — surface known-bad / non-runnable entries
- First-run experience — `aeolus init` and dashboard onboarding
- Better error messages across the API
- Distribution — binary releases and Homebrew tap

## License

MIT — see [LICENSE](./LICENSE).
