# Aeolus

The control plane for AI agents' access to tools.

Aeolus is an open-source proxy that sits between MCP clients (Claude Desktop, Cursor, custom agents) and the MCP servers they call. It aggregates upstream servers, filters which tools each agent sees, and logs every tool call for audit and observability.

> Status: **pre-alpha**. Not yet usable. See the v0.0.1 checklist below.

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

## v0.0.1 checklist

- [ ] Scaffold Go project
- [ ] MCP protocol types and config schema
- [ ] Upstream MCP client (one server, stdio transport)
- [ ] Downstream MCP server (stdio transport)
- [ ] Pass-through forwarding end-to-end
- [ ] Structured JSON logging of tool calls
- [ ] Tool filtering (allow / deny list)
- [ ] README quickstart with a real MCP server

## Roadmap

- **v0.1** — HTTP + SSE transport, multiple upstreams, per-tool latency metrics
- **v0.2** — hosted dashboard (Aeolus Cloud) for telemetry and search
- **v0.3** — policy engine: deny rules, argument redaction, approval gates
- **v1.0** — production-ready, SSO, audit log export

## License

MIT — see [LICENSE](./LICENSE).
