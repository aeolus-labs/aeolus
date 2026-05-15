// Package proxy wires a downstream MCP client to one or more upstream MCP
// servers, aggregating their tool lists with per-upstream name prefixes,
// applying allow/deny filters, and logging every tools/call.
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
	proxyVersion = "0.1.0-dev"
)

type Proxy struct {
	client    *mcp.Conn
	upstreams []*upstream.Upstream
	log       *slog.Logger
	filter    ToolFilter
	observer  Observer

	toolMu  sync.RWMutex
	toolMap map[string]toolEntry // exposed name -> route + tool metadata
}

// ToolCallObservation is emitted after each tools/call response (success or
// error) so observers can record metrics or stream events.
type ToolCallObservation struct {
	Time     time.Time
	Tool     string
	Upstream string
	Latency  time.Duration
	Status   string // "ok" | "error" | "transport_error"
}

// Observer is called once per tools/call completion. nil is allowed.
type Observer func(ToolCallObservation)

type toolEntry struct {
	upstream     *upstream.Upstream
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

func New(client *mcp.Conn, upstreams []*upstream.Upstream, filter ToolFilter, log *slog.Logger, observer Observer) *Proxy {
	return &Proxy{
		client:    client,
		upstreams: upstreams,
		filter:    filter,
		log:       log,
		observer:  observer,
		toolMap:   make(map[string]toolEntry),
	}
}

// Run initializes all upstreams, builds the tool route map, and serves the client.
func (p *Proxy) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	clientInfo := mcp.Info{Name: proxyName, Version: proxyVersion}
	for _, u := range p.upstreams {
		result, err := u.Initialize(ctx, clientInfo)
		if err != nil {
			return fmt.Errorf("initialize %s: %w", u.Name, err)
		}
		p.log.Info("upstream_initialized",
			"name", u.Name,
			"server", result.ServerInfo.Name,
			"protocol", result.ProtocolVersion,
		)
	}

	if err := p.refreshTools(ctx); err != nil {
		return err
	}
	p.log.Info("tools_loaded", "count", p.toolCount())

	return p.serveClient(ctx)
}

func (p *Proxy) toolCount() int {
	p.toolMu.RLock()
	defer p.toolMu.RUnlock()
	return len(p.toolMap)
}

func (p *Proxy) refreshTools(ctx context.Context) error {
	next := make(map[string]toolEntry)
	for _, u := range p.upstreams {
		resp, err := u.Request(ctx, mcp.MethodToolsList, struct{}{})
		if err != nil {
			return fmt.Errorf("tools/list from %s: %w", u.Name, err)
		}
		if resp.Error != nil {
			return fmt.Errorf("tools/list from %s: %s", u.Name, resp.Error.Message)
		}
		var result mcp.ToolsListResult
		if err := json.Unmarshal(resp.Result, &result); err != nil {
			return fmt.Errorf("parse tools/list from %s: %w", u.Name, err)
		}
		prefix := u.Name + "."
		for _, t := range result.Tools {
			exposed := prefix + t.Name
			next[exposed] = toolEntry{
				upstream:     u,
				originalName: t.Name,
				tool: mcp.Tool{
					Name:        exposed,
					Description: t.Description,
					InputSchema: t.InputSchema,
				},
			}
		}
	}
	p.toolMu.Lock()
	p.toolMap = next
	p.toolMu.Unlock()
	return nil
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
		return p.handleNotification(msg)
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

func (p *Proxy) handleNotification(msg *mcp.Message) error {
	// Upstreams are initialized at startup, so client's "initialized" is a no-op.
	// Other notifications are dropped for v0.1.0; broadcasting is a future change.
	return nil
}

func (p *Proxy) replyInitialize(msg *mcp.Message) error {
	result := mcp.InitializeResult{
		ProtocolVersion: mcp.ProtocolVersion,
		Capabilities:    json.RawMessage(`{"tools":{"listChanged":false}}`),
		ServerInfo:      mcp.Info{Name: proxyName, Version: proxyVersion},
	}
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

func (p *Proxy) replyToolsList(msg *mcp.Message) error {
	p.toolMu.RLock()
	tools := make([]mcp.Tool, 0, len(p.toolMap))
	for _, e := range p.toolMap {
		if !p.filter.Allowed(e.tool.Name) {
			continue
		}
		tools = append(tools, e.tool)
	}
	total := len(p.toolMap)
	p.toolMu.RUnlock()

	result := mcp.ToolsListResult{Tools: tools}
	resultRaw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	p.log.Info("tools_list",
		"before", total,
		"after", len(tools),
		"filtered_out", total-len(tools),
	)
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
	p.toolMu.RLock()
	entry, ok := p.toolMap[params.Name]
	p.toolMu.RUnlock()
	if !ok {
		return p.replyError(msg.ID, -32601, "Tool not found: "+params.Name)
	}
	if !p.filter.Allowed(params.Name) {
		p.log.Warn("tools_call_denied", "tool", params.Name)
		return p.replyError(msg.ID, -32601, "Tool not allowed: "+params.Name)
	}

	upstreamParams := mcp.ToolsCallParams{
		Name:      entry.originalName,
		Arguments: params.Arguments,
	}
	started := time.Now()
	resp, err := entry.upstream.Request(ctx, mcp.MethodToolsCall, upstreamParams)
	latency := time.Since(started)
	if err != nil {
		p.log.Error("tools_call",
			"tool", params.Name,
			"upstream", entry.upstream.Name,
			"latency_ms", latency.Milliseconds(),
			"status", "transport_error",
			"error", err.Error(),
		)
		p.observe(params.Name, entry.upstream.Name, latency, "transport_error", started)
		return p.replyError(msg.ID, -32603, "Upstream error: "+err.Error())
	}
	status := "ok"
	if resp.Error != nil {
		status = "error"
	}
	p.log.Info("tools_call",
		"tool", params.Name,
		"upstream", entry.upstream.Name,
		"latency_ms", latency.Milliseconds(),
		"status", status,
	)
	p.observe(params.Name, entry.upstream.Name, latency, status, started)
	return p.client.Write(&mcp.Message{
		JSONRPC: "2.0",
		ID:      msg.ID,
		Result:  resp.Result,
		Error:   resp.Error,
	})
}

func (p *Proxy) observe(tool, upstream string, latency time.Duration, status string, when time.Time) {
	if p.observer == nil {
		return
	}
	p.observer(ToolCallObservation{
		Time:     when,
		Tool:     tool,
		Upstream: upstream,
		Latency:  latency,
		Status:   status,
	})
}

func (p *Proxy) replyError(id json.RawMessage, code int, message string) error {
	return p.client.Write(&mcp.Message{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &mcp.RPCError{Code: code, Message: message},
	})
}
