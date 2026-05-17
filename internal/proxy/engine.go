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
	upstreams []upstream.Server // only successfully-initialized upstreams
	filter    ToolFilter
	cancels   []context.CancelFunc

	toolMu  sync.RWMutex
	toolMap map[string]toolEntry

	subsMu sync.Mutex
	subs   map[chan struct{}]struct{}

	// shutdownCh is closed when Stop() is invoked. Long-lived consumers
	// (the HTTP SSE handler, the stdio bridge via that SSE stream) watch
	// it to send a goodbye notification before the daemon goes away.
	shutdownMu sync.Mutex
	shutdownCh chan struct{}

	// failures records upstreams that couldn't be initialized so the
	// dashboard / API can surface them. Keyed by upstream name.
	failuresMu sync.RWMutex
	failures   map[string]string
}

// UpstreamFailure is a single configured-but-not-running upstream.
type UpstreamFailure struct {
	Name  string `json:"name"`
	Error string `json:"error"`
}

// NewEngine constructs an Engine. Call Start before issuing any
// HandleInitialize / ListTools / CallTool requests.
func NewEngine(upstreams []upstream.Server, filter ToolFilter, log *slog.Logger, observer Observer) *Engine {
	return &Engine{
		log:        log,
		observer:   observer,
		upstreams:  upstreams,
		filter:     filter,
		toolMap:    make(map[string]toolEntry),
		subs:       make(map[chan struct{}]struct{}),
		failures:   make(map[string]string),
		shutdownCh: make(chan struct{}),
	}
}

// Start performs the MCP initialize handshake against every upstream,
// builds the aggregated tool map, and launches per-upstream notification
// watchers. Upstreams that fail to initialize are recorded but do not
// block the daemon — the dashboard stays up, the working upstreams
// serve traffic, and the failures surface in logs and via
// FailedUpstreams(). The daemon as a whole only fails if the runtime
// itself can't proceed (it currently never does).
//
// The watchers keep running until ctx is canceled or Stop is called.
func (e *Engine) Start(ctx context.Context) error {
	working := e.initUpstreams(ctx, e.upstreams)
	e.mu.Lock()
	e.upstreams = working
	e.mu.Unlock()

	e.refreshTools(ctx)
	e.log.Info("upstreams_ready",
		"working", len(working),
		"failed", e.failureCount(),
		"tools", e.toolCount(),
	)

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
// SubscribeToolsChanged channel. Best-effort like Start: upstreams that
// fail to initialize are dropped from the active set and recorded as
// failures, but the reload itself never fails.
func (e *Engine) Reload(ctx context.Context, newUpstreams []upstream.Server, newFilter ToolFilter) error {
	working := e.initUpstreams(ctx, newUpstreams)

	e.mu.Lock()
	oldUpstreams := e.upstreams
	oldCancels := e.cancels
	e.upstreams = working
	e.filter = newFilter
	e.cancels = make([]context.CancelFunc, 0, len(working))
	for _, u := range working {
		u := u
		watcherCtx, c := context.WithCancel(ctx)
		e.cancels = append(e.cancels, c)
		go e.watchUpstreamNotifications(watcherCtx, u)
	}
	e.mu.Unlock()

	e.refreshTools(ctx)
	e.broadcastToolsChanged()

	for _, c := range oldCancels {
		c()
	}
	for _, u := range oldUpstreams {
		u.Shutdown(3 * time.Second)
	}
	e.log.Info("reload_complete",
		"working", len(working),
		"failed", e.failureCount(),
		"tools", e.toolCount(),
	)
	return nil
}

// ReconnectUpstream restarts a single upstream in place without
// disturbing any others. The build closure produces a fresh
// upstream.Server from the caller's config — Engine doesn't know how
// to construct upstreams itself, so the caller (main.go) supplies
// that. On success the old server is shut down only after the new one
// has initialized, so callers never see a window with no upstream
// answering for `name`.
//
// Returns an error if the named upstream isn't currently configured
// (caller should use Reload for that case) or if the fresh server
// fails to initialize.
func (e *Engine) ReconnectUpstream(ctx context.Context, name string, build func() (upstream.Server, error)) error {
	e.mu.Lock()
	var oldIdx = -1
	for i, u := range e.upstreams {
		if u.Name() == name {
			oldIdx = i
			break
		}
	}
	e.mu.Unlock()

	fresh, err := build()
	if err != nil {
		return fmt.Errorf("build upstream %s: %w", name, err)
	}

	clientInfo := mcp.Info{Name: proxyName, Version: proxyVersion}
	if _, err := fresh.Initialize(ctx, clientInfo); err != nil {
		go fresh.Shutdown(2 * time.Second)
		// Treat failed reconnect as a recorded failure so the dashboard
		// can show why instead of silently keeping the old one.
		e.recordFailure(name, err.Error())
		return fmt.Errorf("initialize %s: %w", name, err)
	}

	// Swap atomically: cancel old watcher, replace slot (or append if
	// the upstream was previously disabled / new), start new watcher.
	e.mu.Lock()
	var oldServer upstream.Server
	var oldCancel context.CancelFunc
	if oldIdx >= 0 && oldIdx < len(e.upstreams) {
		oldServer = e.upstreams[oldIdx]
		e.upstreams[oldIdx] = fresh
		if oldIdx < len(e.cancels) {
			oldCancel = e.cancels[oldIdx]
		}
	} else {
		e.upstreams = append(e.upstreams, fresh)
	}
	watcherCtx, c := context.WithCancel(ctx)
	if oldIdx >= 0 && oldIdx < len(e.cancels) {
		e.cancels[oldIdx] = c
	} else {
		e.cancels = append(e.cancels, c)
	}
	e.mu.Unlock()

	// Clear any stale failure for this upstream since reconnect succeeded.
	e.failuresMu.Lock()
	delete(e.failures, name)
	e.failuresMu.Unlock()

	go e.watchUpstreamNotifications(watcherCtx, fresh)
	if oldCancel != nil {
		oldCancel()
	}
	if oldServer != nil {
		// Drop the old upstream's tool entries before fetching new ones.
		// refreshUpstream filters by `entry.upstream == u` and gets
		// passed the *new* upstream — so without this purge the stale
		// tools from the old pointer would linger in toolMap forever.
		e.toolMu.Lock()
		for n, entry := range e.toolMap {
			if entry.upstream == oldServer {
				delete(e.toolMap, n)
			}
		}
		e.toolMu.Unlock()
		go oldServer.Shutdown(3 * time.Second)
	}

	if err := e.refreshUpstream(ctx, fresh); err != nil {
		e.log.Error("reconnect_refresh_failed", "upstream", name, "error", err.Error())
	}
	e.broadcastToolsChanged()
	e.log.Info("upstream_reconnected", "name", name, "tools", e.toolCount())
	return nil
}

// Stop tears down watchers and shuts upstreams down in parallel. Before
// touching upstreams it closes the shutdown channel so connected SSE
// subscribers can emit a goodbye notification — this gives the stdio
// bridge a chance to exit cleanly, which in turn lets the MCP client
// (Claude Code, Cursor, etc.) detect "server is gone" instead of
// silently retaining its connection until the next failed request.
func (e *Engine) Stop(timeout time.Duration) {
	e.shutdownMu.Lock()
	select {
	case <-e.shutdownCh:
		// already signalled
	default:
		close(e.shutdownCh)
	}
	e.shutdownMu.Unlock()

	// Give SSE handlers a brief window to push the goodbye event and
	// flush their buffers before we cancel watchers / kill upstreams.
	time.Sleep(150 * time.Millisecond)

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

// ShuttingDown returns a channel that closes when Stop() is invoked.
// SSE handlers select on it so they can emit a final notification before
// the daemon tears down upstreams.
func (e *Engine) ShuttingDown() <-chan struct{} {
	return e.shutdownCh
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
// response or an MCP-style error message. The observer hook fires once
// with the arguments, response, originating client, and resolved
// workspace so the dashboard can show per-call detail and per-client /
// per-project attribution.
func (e *Engine) CallTool(ctx context.Context, exposed string, arguments json.RawMessage, client, workspace string) *mcp.Message {
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
		errResp, _ := json.Marshal(map[string]string{"transport_error": err.Error()})
		e.observe(exposed, entry.upstream.Name(), latency, "transport_error", started, client, workspace, arguments, errResp)
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
	var responseBody json.RawMessage
	if resp.Error != nil {
		responseBody, _ = json.Marshal(resp.Error)
	} else {
		responseBody = resp.Result
	}
	e.observe(exposed, entry.upstream.Name(), latency, status, started, client, workspace, arguments, responseBody)

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

// maxObservationPayload caps the size of args/response we forward to the
// observer. With a recent-events buffer in the dashboard (~200 entries),
// 16 KiB per side keeps total memory bounded at ~6 MiB worst-case while
// still preserving full payloads for the vast majority of real calls.
const maxObservationPayload = 16 << 10

func (e *Engine) observe(tool, up string, latency time.Duration, status string, when time.Time, client, workspace string, args, response json.RawMessage) {
	if e.observer == nil {
		return
	}
	e.observer(ToolCallObservation{
		Time:      when,
		Tool:      tool,
		Upstream:  up,
		Latency:   latency,
		Status:    status,
		Client:    client,
		Workspace: workspace,
		Arguments: capPayload(args),
		Response:  capPayload(response),
	})
}

// capPayload returns p unchanged if it's small enough to keep, or a
// placeholder JSON value describing the truncated length otherwise.
// Nil / empty input is returned untouched.
func capPayload(p json.RawMessage) json.RawMessage {
	if len(p) == 0 {
		return p
	}
	if len(p) <= maxObservationPayload {
		return p
	}
	placeholder, _ := json.Marshal(map[string]any{
		"_truncated":      true,
		"original_bytes":  len(p),
		"preserved_bytes": maxObservationPayload,
	})
	return placeholder
}

func (e *Engine) toolCount() int {
	e.toolMu.RLock()
	defer e.toolMu.RUnlock()
	return len(e.toolMap)
}

// FailedUpstreams returns the names of upstreams that couldn't be
// initialized or refreshed, along with the error message. Safe to call
// concurrently with Reload — the caller gets a snapshot.
func (e *Engine) FailedUpstreams() []UpstreamFailure {
	e.failuresMu.RLock()
	defer e.failuresMu.RUnlock()
	out := make([]UpstreamFailure, 0, len(e.failures))
	for name, msg := range e.failures {
		out = append(out, UpstreamFailure{Name: name, Error: msg})
	}
	return out
}

func (e *Engine) failureCount() int {
	e.failuresMu.RLock()
	defer e.failuresMu.RUnlock()
	return len(e.failures)
}

// initUpstreams runs the MCP initialize handshake against each
// configured upstream and partitions them into success / failure sets.
// Failed upstreams are shut down (best effort) so their transports
// don't leak; their error message is stashed on e.failures for the
// dashboard to surface. Successful upstreams are returned in input
// order so namespacing is stable across restarts.
func (e *Engine) initUpstreams(ctx context.Context, upstreams []upstream.Server) []upstream.Server {
	clientInfo := mcp.Info{Name: proxyName, Version: proxyVersion}
	working := make([]upstream.Server, 0, len(upstreams))
	failures := make(map[string]string)

	for _, u := range upstreams {
		result, err := u.Initialize(ctx, clientInfo)
		if err != nil {
			e.log.Error("upstream_init_failed",
				"upstream", u.Name(),
				"error", err.Error(),
			)
			failures[u.Name()] = err.Error()
			go u.Shutdown(2 * time.Second)
			continue
		}
		e.log.Info("upstream_initialized",
			"name", u.Name(),
			"server", result.ServerInfo.Name,
			"protocol", result.ProtocolVersion,
		)
		working = append(working, u)
	}

	e.failuresMu.Lock()
	e.failures = failures
	e.failuresMu.Unlock()
	return working
}

// refreshTools rebuilds the aggregated tool map across all working
// upstreams. Per-upstream fetch failures are logged + recorded on
// e.failures but don't poison the whole refresh — tools from healthy
// upstreams stay available.
func (e *Engine) refreshTools(ctx context.Context) {
	e.mu.RLock()
	upstreams := append([]upstream.Server(nil), e.upstreams...)
	e.mu.RUnlock()

	next := make(map[string]toolEntry)
	for _, u := range upstreams {
		if err := fetchTools(ctx, u, next); err != nil {
			e.log.Error("tools_fetch_failed",
				"upstream", u.Name(),
				"error", err.Error(),
			)
			e.recordFailure(u.Name(), err.Error())
		}
	}
	e.toolMu.Lock()
	e.toolMap = next
	e.toolMu.Unlock()
}

func (e *Engine) recordFailure(name, errMsg string) {
	e.failuresMu.Lock()
	if e.failures == nil {
		e.failures = make(map[string]string)
	}
	e.failures[name] = errMsg
	e.failuresMu.Unlock()
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
