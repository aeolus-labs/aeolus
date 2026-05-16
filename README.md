# Aeolus

A local daemon that gateways every MCP server you use through one dashboard.

Aeolus runs as a long-lived background service on your laptop. It exposes one MCP endpoint (Streamable HTTP + a stdio bridge) that aggregates every MCP server you've configured — filesystem, github, slack, postgres, hosted servers, anything that speaks MCP. Any client — Claude Code, Cursor, GitHub Copilot, Zed, Continue, custom agents — connects to that one endpoint instead of wiring up each MCP server individually.

> Status: **v0.4.0 — alpha.** Daemon architecture, single-binary, runs on macOS / Linux / Windows.

## Why

Plain MCP gives you tools but leaves three gaps:

1. **Tool bloat.** Loading every tool from every connected MCP server burns context tokens and degrades model performance.
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

With Aeolus, the consumer talks to one endpoint and the Aeolus daemon fans out:

```
Consumer  ─►  Aeolus daemon  ─►  filesystem MCP   (stdio)
                             ─►  github MCP       (stdio)
                             ─►  postgres MCP     (stdio)
                             ─►  example.com/mcp  (http)
```

Aeolus handles:

- **Aggregation** — many MCP servers, one endpoint
- **Namespacing** — tool names prefixed by upstream (`filesystem.read_file`) so collisions don't happen
- **Filtering** — allow/deny rules per tool, with globs
- **Secret management** — env values and HTTP headers can live in the OS keychain, never in YAML
- **Observability** — every tool call logged; dashboard at `http://localhost:8765`
- **Hot reload** — edit `aeolus.yaml` or use the dashboard editor; changes apply without disconnecting any client

## Install

```bash
# Build from source (until binary releases are published)
git clone https://github.com/aeolus-labs/aeolus.git
cd aeolus
make build
sudo mv ./aeolus /usr/local/bin/
```

Requires Go 1.22+ and Node 18+ to build. Future releases will be downloadable binaries (and eventually `brew install aeolus-labs/tap/aeolus`).

## First run

```bash
# Generate a starter config at ~/.config/aeolus/aeolus.yaml
aeolus init

# Install the launchd service (macOS)
aeolus service install
aeolus service start

# Open the dashboard
aeolus open
```

The dashboard pops up at `http://localhost:8765`. Use it to add your first MCP server (Servers tab → Catalog or `+ Add upstream`). Changes apply immediately — no daemon restart needed.

To check status:

```bash
aeolus service status   # running | loaded, not running | not loaded
aeolus service logs -f  # tail the daemon's stderr
```

To remove:

```bash
aeolus service uninstall
```

If you'd rather run the daemon in the foreground (no launchd):

```bash
aeolus  # reads ~/.config/aeolus/aeolus.yaml by default
```

## Connect your MCP client

The same snippet works for any MCP client that supports stdio (Claude Desktop, Claude Code, Cursor, GitHub Copilot, Zed, Continue, custom agents). The `aeolus mcp` subcommand is a stdio shim that forwards to the running daemon.

### Claude Desktop

`~/Library/Application Support/Claude/claude_desktop_config.json`:

```json
{
  "mcpServers": {
    "aeolus": {
      "command": "aeolus",
      "args": ["mcp"]
    }
  }
}
```

### Claude Code

```bash
claude mcp add aeolus aeolus -- mcp
```

### Cursor

`~/.cursor/mcp.json` (or `.cursor/mcp.json` in a project):

```json
{
  "mcpServers": {
    "aeolus": {
      "command": "aeolus",
      "args": ["mcp"]
    }
  }
}
```

### GitHub Copilot (VS Code)

`.vscode/mcp.json`:

```json
{
  "servers": {
    "aeolus": {
      "type": "stdio",
      "command": "aeolus",
      "args": ["mcp"]
    }
  }
}
```

### Zed

`~/.config/zed/settings.json`:

```json
{
  "context_servers": {
    "aeolus": {
      "command": {
        "path": "aeolus",
        "args": ["mcp"]
      }
    }
  }
}
```

### Continue.dev

`~/.continue/config.json`:

```json
{
  "experimental": {
    "modelContextProtocolServers": [
      {
        "transport": {
          "type": "stdio",
          "command": "aeolus",
          "args": ["mcp"]
        }
      }
    ]
  }
}
```

### Custom agent (Anthropic / OpenAI SDK / local model)

Two options:

**A. Spawn the stdio bridge** (same shape as the snippets above) — works with any MCP client SDK.

**B. Connect directly over HTTP** — Aeolus serves Streamable HTTP at `http://localhost:8765/mcp`:

```ts
import { StreamableHTTPClientTransport } from '@modelcontextprotocol/sdk/client/streamableHttp.js'
import { Client } from '@modelcontextprotocol/sdk/client/index.js'

const transport = new StreamableHTTPClientTransport(new URL('http://localhost:8765/mcp'))
const client = new Client({ name: 'my-agent', version: '0.1.0' })
await client.connect(transport)
```

## Configuration

`~/.config/aeolus/aeolus.yaml` describes the upstream servers, tool rules, logging, and dashboard settings.

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
    - filesystem.read_*
    - github.list_*
    - hosted.*
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

- **stdio** (default) — Aeolus spawns the MCP server as a subprocess.
- **http** — Aeolus POSTs JSON-RPC to a Streamable HTTP endpoint per the MCP spec.

### Secrets in the keychain

Any value in `env:` or `headers:` can be `keychain:<name>`. Aeolus resolves it from the OS keychain (macOS Keychain, Windows Credential Manager, libsecret on Linux) at spawn time. The actual secret never lives in `aeolus.yaml`. The dashboard's 🔓 toggle stores values into the keychain on save.

### Hot reload

Edits to `aeolus.yaml` (by hand or through the dashboard) are picked up immediately. Connected clients get a `tools/list_changed` notification so they refresh. Invalid configs are logged and ignored — the previous good config keeps running.

## The dashboard

Visit `http://localhost:8765` (or run `aeolus open`).

- **Servers** tab: connected upstreams with per-upstream Setup / Tools views; Add / Edit / Remove flow that probes new servers before saving; Catalog browser of ~2,600 MCP servers from the official MCP Registry, filterable by transport and auth.
- **Live** tab: every tool call streamed in real time — time, upstream, tool, latency, status — with filters and per-tool stats (p50/p95).

## CLI reference

```
aeolus                       run as a daemon (foreground)
aeolus service install       write the launchd plist
aeolus service start         start the daemon as a launchd agent
aeolus service stop          stop the daemon
aeolus service status        running / not running
aeolus service restart       stop + start
aeolus service uninstall     stop + remove plist
aeolus service logs [-f]     tail the daemon's stderr

aeolus mcp                   stdio bridge for MCP clients (see snippets above)
aeolus open                  open dashboard in default browser
aeolus init                  write a starter aeolus.yaml
aeolus --version             print version
aeolus --help                full help
```

## Repo layout

```
aeolus/
├── cmd/aeolus/             Go entry point (daemon, mcp bridge, service, open, init)
├── internal/
│   ├── mcp/                JSON-RPC + MCP types
│   ├── config/             YAML loader + validation
│   ├── upstream/           stdio + http upstream server impls
│   ├── proxy/              Engine (long-lived state), Proxy (stdio adapter)
│   ├── secrets/            OS keychain wrapper
│   ├── watcher/            aeolus.yaml file watcher
│   └── dashboard/          HTTP + SSE; embedded React build; /mcp endpoint
├── dashboard/              React source (Vite + TypeScript)
├── examples/               sample configs
└── Makefile                build orchestration
```

## What's next

- macOS `.app` bundle so a Dock icon launches the dashboard.
- Linux systemd / Windows service equivalents to `aeolus service`.
- Auto-update path for installed daemons.
- Distribution: binary releases on GitHub + Homebrew tap.

## License

MIT — see [LICENSE](./LICENSE).
