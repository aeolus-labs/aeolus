// Package proxy wires MCP clients to one or more upstream MCP servers,
// aggregating their tool lists with per-upstream name prefixes, applying
// allow/deny filters, and logging every tools/call.
//
// The long-lived state (upstream pool, tool map, hot reload) lives in
// Engine. Proxy is a thin stdio adapter that connects one MCP client over
// stdio to an Engine. HTTP MCP clients use the dashboard's /mcp endpoint,
// which calls the same Engine directly.
package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/aeolus-labs/aeolus/internal/config"
	"github.com/aeolus-labs/aeolus/internal/mcp"
	"github.com/aeolus-labs/aeolus/internal/upstream"
)

const (
	proxyName    = "aeolus"
	proxyVersion = "0.4.0-dev"
)

// ToolCallObservation is emitted after each tools/call response (success
// or error) so observers can record metrics or stream events.
type ToolCallObservation struct {
	Time      time.Time
	Tool      string
	Upstream  string
	Latency   time.Duration
	Status    string // "ok" | "error" | "transport_error"
	Client    string // MCP client info from initialize, e.g. "claude-code 1.0.0"
	Workspace string // resolved workspace name, empty if none
	Arguments json.RawMessage
	Response  json.RawMessage
}

// Observer is called once per tools/call completion. nil is allowed.
type Observer func(ToolCallObservation)

type toolEntry struct {
	upstream     upstream.Server
	originalName string
	tool         mcp.Tool // tool.Name is the exposed (prefixed) name
}

// ToolFilter decides whether an exposed tool name is shown to the client.
type ToolFilter struct {
	allow []string
	deny  []string
}

func NewToolFilter(cfg config.Tools) ToolFilter {
	return ToolFilter{allow: cfg.Allow, deny: cfg.Deny}
}

// Allowed reports whether name (the exposed, prefixed name) is exposed.
// Patterns support glob matching via path.Match (e.g. "github.create_*").
// If allow is empty, all non-denied tools pass.
func (f ToolFilter) Allowed(name string) bool {
	for _, pat := range f.deny {
		if matches(pat, name) {
			return false
		}
	}
	if len(f.allow) == 0 {
		return true
	}
	for _, pat := range f.allow {
		if matches(pat, name) {
			return true
		}
	}
	return false
}

func matches(pattern, name string) bool {
	if pattern == name {
		return true
	}
	if !strings.ContainsAny(pattern, "*?[") {
		return false
	}
	ok, err := path.Match(pattern, name)
	return err == nil && ok
}

func fetchTools(ctx context.Context, u upstream.Server, into map[string]toolEntry) error {
	resp, err := u.Request(ctx, mcp.MethodToolsList, struct{}{})
	if err != nil {
		return fmt.Errorf("tools/list from %s: %w", u.Name(), err)
	}
	if resp.Error != nil {
		return fmt.Errorf("tools/list from %s: %s", u.Name(), resp.Error.Message)
	}
	var result mcp.ToolsListResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return fmt.Errorf("parse tools/list from %s: %w", u.Name(), err)
	}
	prefix := u.Name() + "."
	for _, t := range result.Tools {
		exposed := prefix + t.Name
		into[exposed] = toolEntry{
			upstream:     u,
			originalName: t.Name,
			tool: mcp.Tool{
				Name:        exposed,
				Description: t.Description,
				InputSchema: t.InputSchema,
			},
		}
	}
	return nil
}

// Proxy adapts one stdio MCP client to an Engine. Multiple Proxy
// instances can share one Engine (each backing a different client).
type Proxy struct {
	engine *Engine
	client *mcp.Conn
	log    *slog.Logger

	clientMu   sync.Mutex
	clientName string // captured on initialize, attached to every tool call
}

// New constructs a Proxy that owns its own Engine. For multi-client
// scenarios (the daemon), construct an Engine separately via NewEngine
// and call NewWithEngine.
func New(client *mcp.Conn, upstreams []upstream.Server, filter ToolFilter, log *slog.Logger, observer Observer) *Proxy {
	return &Proxy{
		engine: NewEngine(upstreams, filter, log, observer),
		client: client,
		log:    log,
	}
}

// NewWithEngine constructs a Proxy that shares an existing Engine.
func NewWithEngine(client *mcp.Conn, engine *Engine, log *slog.Logger) *Proxy {
	return &Proxy{engine: engine, client: client, log: log}
}

// Engine returns the underlying engine — useful for the daemon which
// needs to hand the same engine to other transports.
func (p *Proxy) Engine() *Engine { return p.engine }

// Run starts the engine (if it hasn't been started) and serves the
// client until the connection closes or ctx is canceled.
func (p *Proxy) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	if err := p.engine.Start(ctx); err != nil {
		return err
	}

	// Forward engine-level tools/list_changed notifications to this client.
	sub, unsub := p.engine.SubscribeToolsChanged()
	defer unsub()
	go func() {
		for range sub {
			_ = p.client.Write(&mcp.Message{
				JSONRPC: "2.0",
				Method:  mcp.MethodToolsListChanged,
			})
		}
	}()

	return p.serveClient(ctx)
}

// Reload swaps upstreams + filter on the underlying engine.
func (p *Proxy) Reload(ctx context.Context, newUpstreams []upstream.Server, newFilter ToolFilter) error {
	return p.engine.Reload(ctx, newUpstreams, newFilter)
}

func (p *Proxy) serveClient(ctx context.Context) error {
	for {
		msg, err := p.client.Read()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if err := p.handleClient(ctx, msg); err != nil {
			return err
		}
	}
}

func (p *Proxy) handleClient(ctx context.Context, msg *mcp.Message) error {
	switch {
	case msg.IsRequest():
		return p.handleRequest(ctx, msg)
	case msg.IsNotification():
		return nil // client → server notifications other than initialized are ignored
	}
	return nil
}

func (p *Proxy) handleRequest(ctx context.Context, msg *mcp.Message) error {
	switch msg.Method {
	case mcp.MethodInitialize:
		return p.replyInitialize(msg)
	case mcp.MethodToolsList:
		return p.replyToolsList(msg)
	case mcp.MethodToolsCall:
		return p.routeToolsCall(ctx, msg)
	default:
		return p.replyError(msg.ID, -32601, "Method not found: "+msg.Method)
	}
}

func (p *Proxy) replyInitialize(msg *mcp.Message) error {
	var params mcp.InitializeParams
	if len(msg.Params) > 0 {
		_ = json.Unmarshal(msg.Params, &params)
	}
	p.clientMu.Lock()
	p.clientName = formatClientName(params.ClientInfo)
	p.clientMu.Unlock()

	result := p.engine.HandleInitialize(params.ClientInfo)
	resultRaw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return p.client.Write(&mcp.Message{
		JSONRPC: "2.0",
		ID:      msg.ID,
		Result:  resultRaw,
	})
}

// formatClientName renders the clientInfo from an initialize call into a
// short display string for the dashboard. Falls back to "unknown" when
// the client doesn't send a name (older MCP clients).
func formatClientName(info mcp.Info) string {
	name := strings.TrimSpace(info.Name)
	if name == "" {
		return "unknown"
	}
	if info.Version != "" {
		return name + " " + info.Version
	}
	return name
}

func (p *Proxy) replyToolsList(msg *mcp.Message) error {
	tools := p.engine.ListTools()
	resultRaw, err := json.Marshal(mcp.ToolsListResult{Tools: tools})
	if err != nil {
		return err
	}
	return p.client.Write(&mcp.Message{
		JSONRPC: "2.0",
		ID:      msg.ID,
		Result:  resultRaw,
	})
}

func (p *Proxy) routeToolsCall(ctx context.Context, msg *mcp.Message) error {
	var params mcp.ToolsCallParams
	if len(msg.Params) > 0 {
		if err := json.Unmarshal(msg.Params, &params); err != nil {
			return p.replyError(msg.ID, -32602, "Invalid params: "+err.Error())
		}
	}
	p.clientMu.Lock()
	client := p.clientName
	p.clientMu.Unlock()
	// stdio Proxy doesn't carry a workspace (no header channel). All
	// stdio sessions see every upstream — the daemon's HTTP path is
	// where workspace resolution lives.
	resp := p.engine.CallTool(ctx, params.Name, params.Arguments, client, "")
	resp.ID = msg.ID
	return p.client.Write(resp)
}

func (p *Proxy) replyError(id json.RawMessage, code int, message string) error {
	return p.client.Write(&mcp.Message{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &mcp.RPCError{Code: code, Message: message},
	})
}
