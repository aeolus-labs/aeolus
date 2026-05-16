package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/aeolus-labs/aeolus/internal/mcp"
	"github.com/aeolus-labs/aeolus/internal/upstream"
)

// Engine manages a shared pool of upstream MCP servers plus the
// aggregated tool route map. It's transport-agnostic — stdio clients
// (via *Proxy), HTTP MCP clients, and dashboard reads all route through
// the same Engine. Multiple concurrent MCP clients can share one Engine.
type Engine struct {
	log      *slog.Logger
	observer Observer

	// Protects upstreams + filter + cancels. Read by hot paths via
	// short snapshots; written by Reload.
	mu        sync.RWMutex
	upstreams []upstream.Server
	filter    ToolFilter
	cancels   []context.CancelFunc

	toolMu  sync.RWMutex
	toolMap map[string]toolEntry

	subsMu sync.Mutex
	subs   map[chan struct{}]struct{}
}

// NewEngine constructs an Engine. Call Start before issuing any
// HandleInitialize / ListTools / CallTool requests.
func NewEngine(upstreams []upstream.Server, filter ToolFilter, log *slog.Logger, observer Observer) *Engine {
	return &Engine{
		log:       log,
		observer:  observer,
		upstreams: upstreams,
		filter:    filter,
		toolMap:   make(map[string]toolEntry),
		subs:      make(map[chan struct{}]struct{}),
	}
}

// Start performs the MCP initialize handshake against every upstream,
// builds the aggregated tool map, and launches per-upstream notification
// watchers. Returns when init completes; the watchers keep running until
// ctx is canceled or Stop is called.
func (e *Engine) Start(ctx context.Context) error {
	if err := e.initUpstreams(ctx, e.upstreams); err != nil {
		return err
	}
	if err := e.refreshTools(ctx); err != nil {
		return err
	}
	e.log.Info("tools_loaded", "count", e.toolCount())

	e.mu.Lock()
	for _, u := range e.upstreams {
		u := u
		watcherCtx, c := context.WithCancel(ctx)
		e.cancels = append(e.cancels, c)
		go e.watchUpstreamNotifications(watcherCtx, u)
	}
	e.mu.Unlock()
	return nil
}

// Reload atomically swaps upstreams + filter. Subscribers (e.g. each
// connected MCP client) receive a tools/list_changed nudge via the
// SubscribeToolsChanged channel.
func (e *Engine) Reload(ctx context.Context, newUpstreams []upstream.Server, newFilter ToolFilter) error {
	if err := e.initUpstreams(ctx, newUpstreams); err != nil {
		for _, u := range newUpstreams {
			u.Shutdown(2 * time.Second)
		}
		return err
	}

	e.mu.Lock()
	oldUpstreams := e.upstreams
	oldCancels := e.cancels
	e.upstreams = newUpstreams
	e.filter = newFilter
	e.cancels = make([]context.CancelFunc, 0, len(newUpstreams))
	for _, u := range newUpstreams {
		u := u
		watcherCtx, c := context.WithCancel(ctx)
		e.cancels = append(e.cancels, c)
		go e.watchUpstreamNotifications(watcherCtx, u)
	}
	e.mu.Unlock()

	if err := e.refreshTools(ctx); err != nil {
		e.log.Error("reload_refresh_failed", "error", err.Error())
	}
	e.broadcastToolsChanged()

	for _, c := range oldCancels {
		c()
	}
	for _, u := range oldUpstreams {
		u.Shutdown(3 * time.Second)
	}
	e.log.Info("reload_complete", "upstreams", len(newUpstreams), "tools", e.toolCount())
	return nil
}

// Stop tears down watchers and shuts upstreams down in parallel.
func (e *Engine) Stop(timeout time.Duration) {
	e.mu.Lock()
	cancels := e.cancels
	upstreams := e.upstreams
	e.cancels = nil
	e.mu.Unlock()

	for _, c := range cancels {
		c()
	}
	var wg sync.WaitGroup
	for _, u := range upstreams {
		u := u
		wg.Add(1)
		go func() {
			defer wg.Done()
			u.Shutdown(timeout)
		}()
	}
	wg.Wait()
}

// HandleInitialize composes the response Aeolus returns for a client's
// initialize request. Aeolus advertises only tools/listChanged for v0.3.
func (e *Engine) HandleInitialize(_ mcp.Info) *mcp.InitializeResult {
	return &mcp.InitializeResult{
		ProtocolVersion: mcp.ProtocolVersion,
		Capabilities:    json.RawMessage(`{"tools":{"listChanged":true}}`),
		ServerInfo:      mcp.Info{Name: proxyName, Version: proxyVersion},
	}
}

// ListTools returns the visible (filtered) tool list for a client.
func (e *Engine) ListTools() []mcp.Tool {
	e.mu.RLock()
	filter := e.filter
	e.mu.RUnlock()

	e.toolMu.RLock()
	tools := make([]mcp.Tool, 0, len(e.toolMap))
	for _, entry := range e.toolMap {
		if !filter.Allowed(entry.tool.Name) {
			continue
		}
		tools = append(tools, entry.tool)
	}
	total := len(e.toolMap)
	e.toolMu.RUnlock()

	e.log.Info("tools_list",
		"before", total,
		"after", len(tools),
		"filtered_out", total-len(tools),
	)
	return tools
}

// CallTool routes a tools/call to the owning upstream and returns the
// response or an MCP-style error message. The observer hook fires once.
func (e *Engine) CallTool(ctx context.Context, exposed string, arguments json.RawMessage) *mcp.Message {
	e.toolMu.RLock()
	entry, ok := e.toolMap[exposed]
	e.toolMu.RUnlock()
	if !ok {
		return errorMessage(-32601, "Tool not found: "+exposed)
	}
	e.mu.RLock()
	filter := e.filter
	e.mu.RUnlock()
	if !filter.Allowed(exposed) {
		e.log.Warn("tools_call_denied", "tool", exposed)
		return errorMessage(-32601, "Tool not allowed: "+exposed)
	}

	upstreamParams := mcp.ToolsCallParams{
		Name:      entry.originalName,
		Arguments: arguments,
	}
	started := time.Now()
	resp, err := entry.upstream.Request(ctx, mcp.MethodToolsCall, upstreamParams)
	latency := time.Since(started)

	if err != nil {
		e.log.Error("tools_call",
			"tool", exposed,
			"upstream", entry.upstream.Name(),
			"latency_ms", latency.Milliseconds(),
			"status", "transport_error",
			"error", err.Error(),
		)
		e.observe(exposed, entry.upstream.Name(), latency, "transport_error", started)
		return errorMessage(-32603, "Upstream error: "+err.Error())
	}

	status := "ok"
	if resp.Error != nil {
		status = "error"
	}
	e.log.Info("tools_call",
		"tool", exposed,
		"upstream", entry.upstream.Name(),
		"latency_ms", latency.Milliseconds(),
		"status", status,
	)
	e.observe(exposed, entry.upstream.Name(), latency, status, started)

	return &mcp.Message{
		JSONRPC: "2.0",
		Result:  resp.Result,
		Error:   resp.Error,
	}
}

// SubscribeToolsChanged returns a buffered channel that fires once every
// time the tool list changes. The caller must drain. Call cancel when
// the subscriber goes away.
func (e *Engine) SubscribeToolsChanged() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	e.subsMu.Lock()
	e.subs[ch] = struct{}{}
	e.subsMu.Unlock()
	return ch, func() {
		e.subsMu.Lock()
		if _, ok := e.subs[ch]; ok {
			delete(e.subs, ch)
			close(ch)
		}
		e.subsMu.Unlock()
	}
}

func (e *Engine) broadcastToolsChanged() {
	e.subsMu.Lock()
	defer e.subsMu.Unlock()
	for ch := range e.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (e *Engine) observe(tool, up string, latency time.Duration, status string, when time.Time) {
	if e.observer == nil {
		return
	}
	e.observer(ToolCallObservation{
		Time:     when,
		Tool:     tool,
		Upstream: up,
		Latency:  latency,
		Status:   status,
	})
}

func (e *Engine) toolCount() int {
	e.toolMu.RLock()
	defer e.toolMu.RUnlock()
	return len(e.toolMap)
}

func (e *Engine) initUpstreams(ctx context.Context, upstreams []upstream.Server) error {
	clientInfo := mcp.Info{Name: proxyName, Version: proxyVersion}
	for _, u := range upstreams {
		result, err := u.Initialize(ctx, clientInfo)
		if err != nil {
			return fmt.Errorf("initialize %s: %w", u.Name(), err)
		}
		e.log.Info("upstream_initialized",
			"name", u.Name(),
			"server", result.ServerInfo.Name,
			"protocol", result.ProtocolVersion,
		)
	}
	return nil
}

func (e *Engine) refreshTools(ctx context.Context) error {
	e.mu.RLock()
	upstreams := append([]upstream.Server(nil), e.upstreams...)
	e.mu.RUnlock()

	next := make(map[string]toolEntry)
	for _, u := range upstreams {
		if err := fetchTools(ctx, u, next); err != nil {
			return err
		}
	}
	e.toolMu.Lock()
	e.toolMap = next
	e.toolMu.Unlock()
	return nil
}

func (e *Engine) refreshUpstream(ctx context.Context, u upstream.Server) error {
	fresh := make(map[string]toolEntry)
	if err := fetchTools(ctx, u, fresh); err != nil {
		return err
	}
	e.toolMu.Lock()
	for name, entry := range e.toolMap {
		if entry.upstream == u {
			delete(e.toolMap, name)
		}
	}
	for name, entry := range fresh {
		e.toolMap[name] = entry
	}
	e.toolMu.Unlock()
	return nil
}

func (e *Engine) watchUpstreamNotifications(ctx context.Context, u upstream.Server) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-u.Notifications():
			if !ok {
				return
			}
			if msg.Method == mcp.MethodToolsListChanged {
				e.log.Info("upstream_tools_changed", "upstream", u.Name())
				if err := e.refreshUpstream(ctx, u); err != nil {
					e.log.Error("refresh_failed", "upstream", u.Name(), "error", err.Error())
					continue
				}
				e.broadcastToolsChanged()
			}
		}
	}
}

func errorMessage(code int, message string) *mcp.Message {
	return &mcp.Message{
		JSONRPC: "2.0",
		Error:   &mcp.RPCError{Code: code, Message: message},
	}
}
