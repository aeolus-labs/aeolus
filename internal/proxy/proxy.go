// Package proxy wires a downstream MCP client to an upstream MCP server,
// filtering tools/list responses and logging tools/call invocations.
package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/aeolus-labs/aeolus/internal/config"
	"github.com/aeolus-labs/aeolus/internal/mcp"
)

type Proxy struct {
	client   *mcp.Conn
	upstream *mcp.Conn
	log      *slog.Logger
	filter   ToolFilter

	mu       sync.Mutex
	inflight map[string]pendingCall
}

type pendingCall struct {
	method   string
	toolName string
	started  time.Time
}

type ToolFilter struct {
	allow []string
	deny  []string
}

func NewToolFilter(cfg config.Tools) ToolFilter {
	return ToolFilter{allow: cfg.Allow, deny: cfg.Deny}
}

// Allowed reports whether tool name is exposed to the client.
// Patterns support glob matching via path.Match (e.g. "read_*").
// If allow is empty, all tools are allowed by default (deny still applies).
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

func New(client, upstream *mcp.Conn, filter ToolFilter, log *slog.Logger) *Proxy {
	return &Proxy{
		client:   client,
		upstream: upstream,
		filter:   filter,
		log:      log,
		inflight: make(map[string]pendingCall),
	}
}

// Run pumps messages in both directions until one side closes or ctx is canceled.
func (p *Proxy) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	errCh := make(chan error, 2)
	go func() { errCh <- p.pump(p.client, p.upstream, p.onClientToUpstream) }()
	go func() { errCh <- p.pump(p.upstream, p.client, p.onUpstreamToClient) }()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

type interceptor func(*mcp.Message) (*mcp.Message, error)

func (p *Proxy) pump(src, dst *mcp.Conn, intercept interceptor) error {
	for {
		msg, err := src.Read()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		msg, err = intercept(msg)
		if err != nil {
			return err
		}
		if msg == nil {
			continue
		}
		if err := dst.Write(msg); err != nil {
			return err
		}
	}
}

func (p *Proxy) onClientToUpstream(msg *mcp.Message) (*mcp.Message, error) {
	if !msg.IsRequest() {
		return msg, nil
	}
	switch msg.Method {
	case mcp.MethodToolsList:
		p.track(msg, "")
	case mcp.MethodToolsCall:
		var params mcp.ToolsCallParams
		if len(msg.Params) > 0 {
			_ = json.Unmarshal(msg.Params, &params)
		}
		p.track(msg, params.Name)
	}
	return msg, nil
}

func (p *Proxy) onUpstreamToClient(msg *mcp.Message) (*mcp.Message, error) {
	if !msg.IsResponse() {
		return msg, nil
	}
	pending, ok := p.take(msg)
	if !ok {
		return msg, nil
	}
	switch pending.method {
	case mcp.MethodToolsList:
		return p.filterToolsList(msg)
	case mcp.MethodToolsCall:
		p.logCall(pending, msg)
	}
	return msg, nil
}

func (p *Proxy) track(msg *mcp.Message, toolName string) {
	key := idKey(msg.ID)
	if key == "" {
		return
	}
	p.mu.Lock()
	p.inflight[key] = pendingCall{
		method:   msg.Method,
		toolName: toolName,
		started:  time.Now(),
	}
	p.mu.Unlock()
}

func (p *Proxy) take(msg *mcp.Message) (pendingCall, bool) {
	key := idKey(msg.ID)
	if key == "" {
		return pendingCall{}, false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	pending, ok := p.inflight[key]
	if ok {
		delete(p.inflight, key)
	}
	return pending, ok
}

func idKey(id json.RawMessage) string {
	if len(id) == 0 {
		return ""
	}
	return string(id)
}

func (p *Proxy) filterToolsList(msg *mcp.Message) (*mcp.Message, error) {
	if msg.Error != nil || len(msg.Result) == 0 {
		return msg, nil
	}
	var result mcp.ToolsListResult
	if err := json.Unmarshal(msg.Result, &result); err != nil {
		return msg, nil
	}
	before := len(result.Tools)
	kept := make([]mcp.Tool, 0, before)
	for _, t := range result.Tools {
		if p.filter.Allowed(t.Name) {
			kept = append(kept, t)
		}
	}
	result.Tools = kept
	newResult, err := json.Marshal(result)
	if err != nil {
		return msg, nil
	}
	msg.Result = newResult
	p.log.Info("tools_list",
		"before", before,
		"after", len(kept),
		"filtered_out", before-len(kept),
	)
	return msg, nil
}

func (p *Proxy) logCall(pending pendingCall, msg *mcp.Message) {
	latency := time.Since(pending.started)
	status := "ok"
	if msg.Error != nil {
		status = "error"
	}
	attrs := []any{
		"tool", pending.toolName,
		"latency_ms", latency.Milliseconds(),
		"status", status,
	}
	if msg.Error != nil {
		attrs = append(attrs, "error_code", msg.Error.Code, "error_message", msg.Error.Message)
	}
	p.log.Info("tools_call", attrs...)
}
