// Package upstream manages MCP servers running as stdio subprocesses,
// exposing a request/response API on top of the raw JSON-RPC wire.
package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aeolus-labs/aeolus/internal/mcp"
	"github.com/aeolus-labs/aeolus/internal/secrets"
)

// keychainPrefix marks an env value that should be resolved from the OS
// keychain at spawn time. e.g. GITHUB_TOKEN=keychain:github.GITHUB_TOKEN
const keychainPrefix = "keychain:"

// resolveEnv replaces any "keychain:<name>" values with the actual secret
// retrieved from the system keychain. Returns an error if a referenced
// secret is missing — the caller should surface that to the operator
// instead of silently spawning a server with a half-configured environment.
func resolveEnv(env []string) ([]string, error) {
	if len(env) == 0 {
		return env, nil
	}
	out := make([]string, len(env))
	for i, e := range env {
		eq := strings.Index(e, "=")
		if eq < 0 {
			out[i] = e
			continue
		}
		key, value := e[:eq], e[eq+1:]
		if !strings.HasPrefix(value, keychainPrefix) {
			out[i] = e
			continue
		}
		ref := strings.TrimPrefix(value, keychainPrefix)
		secret, err := secrets.Get(ref)
		if err != nil {
			return nil, fmt.Errorf("resolve %s=keychain:%s: %w", key, ref, err)
		}
		out[i] = key + "=" + secret
	}
	return out, nil
}

// Upstream is a connected MCP server.
type Upstream struct {
	Name string

	cmd         *exec.Cmd     // nil for non-subprocess upstreams (tests)
	stdinCloser io.Closer     // nil for non-subprocess upstreams
	conn        *mcp.Conn
	stderr      io.ReadCloser // nil for non-subprocess upstreams
	log         *slog.Logger

	nextID atomic.Int64

	mu       sync.Mutex
	pending  map[int64]chan *mcp.Message
	notif    chan *mcp.Message
	closed   bool
	closeErr error

	Done chan struct{}
}

// FromConn builds an Upstream from an existing MCP connection. The caller
// owns the underlying transport; Upstream will not close it. Intended for
// tests and future non-subprocess transports.
func FromConn(name string, conn *mcp.Conn, log *slog.Logger) *Upstream {
	u := &Upstream{
		Name:    name,
		conn:    conn,
		log:     log,
		pending: make(map[int64]chan *mcp.Message),
		notif:   make(chan *mcp.Message, 16),
		Done:    make(chan struct{}),
	}
	go u.readLoop()
	return u
}

// Start launches the MCP server subprocess and begins reading its stdout.
// The returned Upstream is connected but not yet initialized — call Initialize.
// env entries are appended to the inherited process environment.
func Start(ctx context.Context, name, command string, args []string, env []string, log *slog.Logger) (*Upstream, error) {
	resolvedEnv, err := resolveEnv(env)
	if err != nil {
		return nil, fmt.Errorf("upstream %s: %w", name, err)
	}
	cmd := exec.CommandContext(ctx, command, args...)
	if len(resolvedEnv) > 0 {
		cmd.Env = append(cmd.Environ(), resolvedEnv...)
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("upstream %s: stdin pipe: %w", name, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("upstream %s: stdout pipe: %w", name, err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("upstream %s: stderr pipe: %w", name, err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("upstream %s: start: %w", name, err)
	}
	u := FromConn(name, mcp.NewConn(stdout, stdin), log)
	u.cmd = cmd
	u.stdinCloser = stdin
	u.stderr = stderr
	return u, nil
}

func (u *Upstream) Stderr() io.Reader {
	if u.stderr == nil {
		return nil
	}
	return u.stderr
}

func (u *Upstream) Wait() error {
	if u.cmd == nil {
		return nil
	}
	return u.cmd.Wait()
}

// Shutdown closes the upstream's stdin so the subprocess can exit cleanly,
// then waits up to timeout for it to do so. After timeout the OS will kill
// the process via exec.CommandContext.
func (u *Upstream) Shutdown(timeout time.Duration) {
	if u.stdinCloser != nil {
		_ = u.stdinCloser.Close()
	}
	if u.cmd == nil {
		return
	}
	done := make(chan struct{})
	go func() {
		_ = u.cmd.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
	}
}

// Notifications returns a channel of upstream-originated notifications.
// Slow consumers will see messages dropped (with a warning log).
func (u *Upstream) Notifications() <-chan *mcp.Message { return u.notif }

// Initialize performs the MCP initialize handshake.
func (u *Upstream) Initialize(ctx context.Context, clientInfo mcp.Info) (*mcp.InitializeResult, error) {
	params := mcp.InitializeParams{
		ProtocolVersion: mcp.ProtocolVersion,
		Capabilities:    json.RawMessage(`{}`),
		ClientInfo:      clientInfo,
	}
	resp, err := u.Request(ctx, mcp.MethodInitialize, params)
	if err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("initialize: %s", resp.Error.Message)
	}
	var result mcp.InitializeResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("parse initialize result: %w", err)
	}
	if err := u.Notify(mcp.MethodInitialized, nil); err != nil {
		return nil, fmt.Errorf("send initialized notification: %w", err)
	}
	return &result, nil
}

// Request sends a JSON-RPC request and waits for the matching response.
func (u *Upstream) Request(ctx context.Context, method string, params any) (*mcp.Message, error) {
	id := u.nextID.Add(1)
	ch := make(chan *mcp.Message, 1)

	u.mu.Lock()
	if u.closed {
		u.mu.Unlock()
		return nil, fmt.Errorf("upstream %s closed: %w", u.Name, u.closeErr)
	}
	u.pending[id] = ch
	u.mu.Unlock()

	var paramsRaw json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			u.dropPending(id)
			return nil, err
		}
		paramsRaw = b
	}
	idRaw, _ := json.Marshal(id)
	if err := u.conn.Write(&mcp.Message{
		JSONRPC: "2.0",
		ID:      idRaw,
		Method:  method,
		Params:  paramsRaw,
	}); err != nil {
		u.dropPending(id)
		return nil, err
	}

	select {
	case resp, ok := <-ch:
		if !ok {
			return nil, fmt.Errorf("upstream %s closed: %w", u.Name, u.closeErr)
		}
		return resp, nil
	case <-ctx.Done():
		u.dropPending(id)
		return nil, ctx.Err()
	}
}

// Notify sends a JSON-RPC notification (no ID, no response).
func (u *Upstream) Notify(method string, params any) error {
	var paramsRaw json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return err
		}
		paramsRaw = b
	}
	return u.conn.Write(&mcp.Message{
		JSONRPC: "2.0",
		Method:  method,
		Params:  paramsRaw,
	})
}

func (u *Upstream) readLoop() {
	defer close(u.Done)
	for {
		msg, err := u.conn.Read()
		if err != nil {
			if errors.Is(err, io.EOF) {
				err = io.EOF
			}
			u.failPending(err)
			return
		}
		switch {
		case msg.IsResponse():
			id, ok := parseID(msg.ID)
			if !ok {
				continue
			}
			u.mu.Lock()
			ch := u.pending[id]
			delete(u.pending, id)
			u.mu.Unlock()
			if ch != nil {
				ch <- msg
			}
		case msg.IsNotification():
			select {
			case u.notif <- msg:
			default:
				u.log.Warn("upstream_notification_dropped", "upstream", u.Name, "method", msg.Method)
			}
		}
	}
}

func (u *Upstream) failPending(err error) {
	u.mu.Lock()
	u.closed = true
	u.closeErr = err
	for id, ch := range u.pending {
		close(ch)
		delete(u.pending, id)
	}
	u.mu.Unlock()
}

func (u *Upstream) dropPending(id int64) {
	u.mu.Lock()
	delete(u.pending, id)
	u.mu.Unlock()
}

func parseID(raw json.RawMessage) (int64, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	var n int64
	if err := json.Unmarshal(raw, &n); err == nil {
		return n, true
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return n, true
		}
	}
	return 0, false
}
