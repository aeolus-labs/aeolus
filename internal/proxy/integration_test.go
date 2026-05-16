package proxy_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aeolus-labs/aeolus/internal/config"
	"github.com/aeolus-labs/aeolus/internal/mcp"
	"github.com/aeolus-labs/aeolus/internal/proxy"
	"github.com/aeolus-labs/aeolus/internal/upstream"
)

// fakeUpstream is an in-memory MCP server for testing.
type fakeUpstream struct {
	name       string
	serverInfo mcp.Info

	mu    sync.Mutex
	tools []mcp.Tool
	conn  *mcp.Conn // set when run() starts

	calls atomic.Int64 // number of tools/call requests received
}

func (f *fakeUpstream) run(conn *mcp.Conn) {
	f.mu.Lock()
	f.conn = conn
	f.mu.Unlock()
	for {
		msg, err := conn.Read()
		if err != nil {
			return
		}
		if !msg.IsRequest() {
			continue
		}
		switch msg.Method {
		case mcp.MethodInitialize:
			f.reply(conn, msg.ID, mcp.InitializeResult{
				ProtocolVersion: mcp.ProtocolVersion,
				Capabilities:    json.RawMessage(`{"tools":{"listChanged":true}}`),
				ServerInfo:      f.serverInfo,
			})
		case mcp.MethodToolsList:
			f.mu.Lock()
			tools := append([]mcp.Tool(nil), f.tools...)
			f.mu.Unlock()
			f.reply(conn, msg.ID, mcp.ToolsListResult{Tools: tools})
		case mcp.MethodToolsCall:
			f.calls.Add(1)
			f.reply(conn, msg.ID, map[string]json.RawMessage{"echo": msg.Params})
		}
	}
}

func (f *fakeUpstream) reply(conn *mcp.Conn, id json.RawMessage, result any) {
	b, _ := json.Marshal(result)
	_ = conn.Write(&mcp.Message{JSONRPC: "2.0", ID: id, Result: b})
}

// setTools updates the fake's tool list and emits tools/list_changed.
func (f *fakeUpstream) setTools(tools []mcp.Tool) {
	f.mu.Lock()
	f.tools = tools
	conn := f.conn
	f.mu.Unlock()
	if conn != nil {
		_ = conn.Write(&mcp.Message{
			JSONRPC: "2.0",
			Method:  mcp.MethodToolsListChanged,
		})
	}
}

type rig struct {
	t          *testing.T
	client     *mcp.Conn
	fakes      []*fakeUpstream
	cancel     context.CancelFunc
	runErr     chan error
	nextID     atomic.Int64
	closeOnce  func()
}

func (r *rig) Close() { r.closeOnce() }

// newRig wires an in-memory client conn to a Proxy that fronts the given
// fake upstreams. The returned client conn behaves like an MCP client.
func newRig(t *testing.T, tools config.Tools, fakes ...*fakeUpstream) *rig {
	t.Helper()

	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	upstreams := make([]upstream.Server, 0, len(fakes))
	upPipes := make([]net.Conn, 0, len(fakes)*2)
	for _, f := range fakes {
		proxySide, fakeSide := net.Pipe()
		upConn := mcp.NewConn(proxySide, proxySide)
		fakeConn := mcp.NewConn(fakeSide, fakeSide)
		u := upstream.FromConn(f.name, upConn, log)
		upstreams = append(upstreams, u)
		go f.run(fakeConn)
		upPipes = append(upPipes, proxySide, fakeSide)
	}

	clientSide, proxySide := net.Pipe()
	clientConn := mcp.NewConn(clientSide, clientSide)
	proxyClientConn := mcp.NewConn(proxySide, proxySide)

	filter := proxy.NewToolFilter(tools)
	p := proxy.New(proxyClientConn, upstreams, filter, log, nil)

	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- p.Run(ctx) }()

	closed := false
	closeOnce := func() {
		if closed {
			return
		}
		closed = true
		cancel()
		_ = clientSide.Close()
		_ = proxySide.Close()
		for _, p := range upPipes {
			_ = p.Close()
		}
	}
	t.Cleanup(closeOnce)

	return &rig{
		t:         t,
		client:    clientConn,
		fakes:     fakes,
		cancel:    cancel,
		runErr:    runErr,
		closeOnce: closeOnce,
	}
}

// sendRequest sends a JSON-RPC request and returns the matching response.
func (r *rig) sendRequest(method string, params any) *mcp.Message {
	r.t.Helper()
	id := r.nextID.Add(1)
	idRaw, _ := json.Marshal(id)
	var paramsRaw json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			r.t.Fatalf("marshal params: %v", err)
		}
		paramsRaw = b
	}
	msg := &mcp.Message{JSONRPC: "2.0", ID: idRaw, Method: method, Params: paramsRaw}
	if err := r.client.Write(msg); err != nil {
		r.t.Fatalf("client write: %v", err)
	}
	deadline := time.After(2 * time.Second)
	for {
		readCh := make(chan *mcp.Message, 1)
		errCh := make(chan error, 1)
		go func() {
			m, err := r.client.Read()
			if err != nil {
				errCh <- err
				return
			}
			readCh <- m
		}()
		select {
		case <-deadline:
			r.t.Fatalf("timeout waiting for response to %s", method)
		case err := <-errCh:
			r.t.Fatalf("client read: %v", err)
		case m := <-readCh:
			if m.IsNotification() {
				continue
			}
			if string(m.ID) != strconv.FormatInt(id, 10) {
				continue
			}
			return m
		}
	}
}

// --- Tests ---

func TestProxy_Initialize(t *testing.T) {
	r := newRig(t, config.Tools{},
		&fakeUpstream{name: "fs", serverInfo: mcp.Info{Name: "fs", Version: "1"}},
	)
	resp := r.sendRequest(mcp.MethodInitialize, mcp.InitializeParams{
		ProtocolVersion: mcp.ProtocolVersion,
		Capabilities:    json.RawMessage(`{}`),
		ClientInfo:      mcp.Info{Name: "test", Version: "0"},
	})
	if resp.Error != nil {
		t.Fatalf("initialize error: %v", resp.Error)
	}
	var result mcp.InitializeResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result.ServerInfo.Name != "aeolus" {
		t.Errorf("server name: got %q, want aeolus", result.ServerInfo.Name)
	}
	if result.ProtocolVersion != mcp.ProtocolVersion {
		t.Errorf("protocol: got %q, want %q", result.ProtocolVersion, mcp.ProtocolVersion)
	}
}

func TestProxy_ToolsListAggregation(t *testing.T) {
	fs := &fakeUpstream{
		name:       "fs",
		serverInfo: mcp.Info{Name: "fs"},
		tools: []mcp.Tool{
			{Name: "read_file", Description: "Read a file"},
			{Name: "write_file"},
		},
	}
	gh := &fakeUpstream{
		name:       "gh",
		serverInfo: mcp.Info{Name: "gh"},
		tools: []mcp.Tool{
			{Name: "create_issue"},
			{Name: "list_issues"},
		},
	}
	r := newRig(t, config.Tools{}, fs, gh)

	resp := r.sendRequest(mcp.MethodToolsList, struct{}{})
	if resp.Error != nil {
		t.Fatalf("tools/list error: %v", resp.Error)
	}
	var result mcp.ToolsListResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	names := toolNames(result.Tools)
	want := map[string]bool{
		"fs.read_file":     true,
		"fs.write_file":    true,
		"gh.create_issue":  true,
		"gh.list_issues":   true,
	}
	if len(names) != len(want) {
		t.Fatalf("tool count: got %d (%v), want %d", len(names), names, len(want))
	}
	for _, n := range names {
		if !want[n] {
			t.Errorf("unexpected tool %q", n)
		}
	}
}

func TestProxy_ToolsListFilter(t *testing.T) {
	fs := &fakeUpstream{
		name:       "fs",
		serverInfo: mcp.Info{Name: "fs"},
		tools:      []mcp.Tool{{Name: "read_file"}, {Name: "write_file"}, {Name: "delete_file"}},
	}
	r := newRig(t, config.Tools{
		Allow: []string{"fs.read_*"},
	}, fs)

	resp := r.sendRequest(mcp.MethodToolsList, struct{}{})
	var result mcp.ToolsListResult
	_ = json.Unmarshal(resp.Result, &result)
	names := toolNames(result.Tools)
	if len(names) != 1 || names[0] != "fs.read_file" {
		t.Errorf("filtered tools: got %v, want [fs.read_file]", names)
	}
}

func TestProxy_ToolsCallRouting(t *testing.T) {
	fs := &fakeUpstream{
		name:       "fs",
		serverInfo: mcp.Info{Name: "fs"},
		tools:      []mcp.Tool{{Name: "read_file"}},
	}
	gh := &fakeUpstream{
		name:       "gh",
		serverInfo: mcp.Info{Name: "gh"},
		tools:      []mcp.Tool{{Name: "create_issue"}},
	}
	r := newRig(t, config.Tools{}, fs, gh)

	// Call the fs tool.
	resp := r.sendRequest(mcp.MethodToolsCall, mcp.ToolsCallParams{
		Name:      "fs.read_file",
		Arguments: json.RawMessage(`{"path":"/etc/hosts"}`),
	})
	if resp.Error != nil {
		t.Fatalf("tools/call error: %v", resp.Error)
	}
	if fs.calls.Load() != 1 || gh.calls.Load() != 0 {
		t.Errorf("call routing wrong: fs=%d gh=%d", fs.calls.Load(), gh.calls.Load())
	}

	// Verify the upstream saw the un-prefixed name in its forwarded params.
	var echo struct {
		Echo struct {
			Name string `json:"name"`
		} `json:"echo"`
	}
	if err := json.Unmarshal(resp.Result, &echo); err != nil {
		t.Fatalf("unmarshal echo: %v", err)
	}
	if echo.Echo.Name != "read_file" {
		t.Errorf("forwarded tool name: got %q, want %q", echo.Echo.Name, "read_file")
	}

	// Now route to the github upstream.
	r.sendRequest(mcp.MethodToolsCall, mcp.ToolsCallParams{Name: "gh.create_issue"})
	if fs.calls.Load() != 1 || gh.calls.Load() != 1 {
		t.Errorf("call routing wrong: fs=%d gh=%d", fs.calls.Load(), gh.calls.Load())
	}
}

func TestProxy_UnknownTool(t *testing.T) {
	fs := &fakeUpstream{
		name:       "fs",
		serverInfo: mcp.Info{Name: "fs"},
		tools:      []mcp.Tool{{Name: "read_file"}},
	}
	r := newRig(t, config.Tools{}, fs)

	resp := r.sendRequest(mcp.MethodToolsCall, mcp.ToolsCallParams{Name: "fs.nonexistent"})
	if resp.Error == nil {
		t.Fatalf("expected error for unknown tool")
	}
	if resp.Error.Code != -32601 {
		t.Errorf("error code: got %d, want -32601", resp.Error.Code)
	}
}

func TestProxy_DeniedToolCannotBeCalled(t *testing.T) {
	fs := &fakeUpstream{
		name:       "fs",
		serverInfo: mcp.Info{Name: "fs"},
		tools:      []mcp.Tool{{Name: "delete_file"}},
	}
	r := newRig(t, config.Tools{
		Deny: []string{"fs.delete_*"},
	}, fs)

	resp := r.sendRequest(mcp.MethodToolsCall, mcp.ToolsCallParams{Name: "fs.delete_file"})
	if resp.Error == nil {
		t.Fatalf("expected error for denied tool")
	}
	if fs.calls.Load() != 0 {
		t.Errorf("denied tool was called: fs=%d", fs.calls.Load())
	}
}

func TestProxy_ToolsListChanged(t *testing.T) {
	fs := &fakeUpstream{
		name:       "fs",
		serverInfo: mcp.Info{Name: "fs"},
		tools:      []mcp.Tool{{Name: "read_file"}},
	}
	r := newRig(t, config.Tools{}, fs)

	resp := r.sendRequest(mcp.MethodToolsList, struct{}{})
	var result mcp.ToolsListResult
	_ = json.Unmarshal(resp.Result, &result)
	if len(result.Tools) != 1 {
		t.Fatalf("initial tools: got %d, want 1", len(result.Tools))
	}

	fs.setTools([]mcp.Tool{{Name: "read_file"}, {Name: "write_file"}})

	// Poll until the proxy reflects the new tools, up to 1s.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		resp = r.sendRequest(mcp.MethodToolsList, struct{}{})
		_ = json.Unmarshal(resp.Result, &result)
		if len(result.Tools) == 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(result.Tools) != 2 {
		t.Fatalf("tools after change: got %d, want 2", len(result.Tools))
	}
}

func toolNames(ts []mcp.Tool) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.Name
	}
	return out
}
