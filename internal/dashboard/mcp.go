package dashboard

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/aeolus-labs/aeolus/internal/mcp"
)

// handleMCP is the inbound Streamable HTTP transport endpoint:
//
//	POST   /mcp   client → server JSON-RPC (returns JSON or 202)
//	GET    /mcp   long-lived SSE for server → client notifications
//	DELETE /mcp   terminate a session
//
// Session continuity is tracked via the Mcp-Session-Id header per the
// MCP spec. The first POST (initialize) gets a fresh session id back
// in the response headers; the client echoes it on subsequent requests.
func (s *Server) handleMCP(w http.ResponseWriter, r *http.Request) {
	if s.engine == nil {
		http.Error(w, "MCP endpoint is not enabled — this build has no engine wired in.", http.StatusServiceUnavailable)
		return
	}
	switch r.Method {
	case http.MethodPost:
		s.handleMCPPost(w, r)
	case http.MethodGet:
		s.handleMCPGet(w, r)
	case http.MethodDelete:
		s.handleMCPDelete(w, r)
	default:
		http.Error(w, "MCP endpoint supports POST, GET, and DELETE.", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleMCPPost(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20)) // 4 MiB cap
	if err != nil {
		http.Error(w, "Couldn't read request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	var msg mcp.Message
	if err := json.Unmarshal(body, &msg); err != nil {
		http.Error(w, "Couldn't parse JSON-RPC: "+err.Error(), http.StatusBadRequest)
		return
	}

	sessID := r.Header.Get("Mcp-Session-Id")
	if sessID == "" {
		if msg.Method != mcp.MethodInitialize {
			http.Error(w,
				"First request on an MCP HTTP session must be an `initialize` call. Provide an Mcp-Session-Id header to use an existing session.",
				http.StatusBadRequest)
			return
		}
		sessID = newSessionID()
		s.openSession(sessID)
		// Resolve workspace from the bridge's request headers. Explicit
		// X-Aeolus-Workspace wins; otherwise we match X-Aeolus-Cwd
		// against configured workspace cwd_match patterns; otherwise we
		// fall back to a workspace literally named "default", and
		// finally to "global" (unscoped) view.
		s.cfgMu.RLock()
		cfg := s.cfg
		s.cfgMu.RUnlock()
		workspace := ""
		if cfg != nil {
			workspace = cfg.ResolveWorkspace(
				strings.TrimSpace(r.Header.Get("X-Aeolus-Workspace")),
				strings.TrimSpace(r.Header.Get("X-Aeolus-Cwd")),
			)
		}
		s.setSessionWorkspace(sessID, workspace)
		w.Header().Set("Mcp-Session-Id", sessID)
	} else if !s.touchSession(sessID) {
		http.Error(w, "Unknown or expired session — start a new one with an initialize call.", http.StatusBadRequest)
		return
	}

	if msg.IsNotification() {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	resp := s.routeMCPRequest(r.Context(), sessID, &msg)
	resp.JSONRPC = "2.0"
	resp.ID = msg.ID

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// routeMCPRequest dispatches an MCP request to the engine and returns the
// response. The returned message has no JSONRPC/ID set; the caller fills
// those in. sessID is used to bind clientInfo on initialize and to look
// it back up on subsequent tool calls so the dashboard can attribute
// each call to the right MCP client.
func (s *Server) routeMCPRequest(ctx context.Context, sessID string, msg *mcp.Message) *mcp.Message {
	switch msg.Method {
	case mcp.MethodInitialize:
		var params mcp.InitializeParams
		if len(msg.Params) > 0 {
			_ = json.Unmarshal(msg.Params, &params)
		}
		s.setSessionClient(sessID, formatHTTPClientName(params.ClientInfo))
		result := s.engine.HandleInitialize(params.ClientInfo)
		b, _ := json.Marshal(result)
		return &mcp.Message{Result: b}

	case mcp.MethodToolsList:
		workspace := s.sessionWorkspace(sessID)
		tools := s.engine.ListTools()
		tools = s.filterToolsByScope(tools, workspace)
		b, _ := json.Marshal(mcp.ToolsListResult{Tools: tools})
		return &mcp.Message{Result: b}

	case mcp.MethodToolsCall:
		var params mcp.ToolsCallParams
		if len(msg.Params) > 0 {
			if err := json.Unmarshal(msg.Params, &params); err != nil {
				return &mcp.Message{Error: &mcp.RPCError{Code: -32602, Message: "Invalid params: " + err.Error()}}
			}
		}
		workspace := s.sessionWorkspace(sessID)
		if !s.toolAllowedInScope(params.Name, workspace) {
			scopeLabel := workspace
			if scopeLabel == "" {
				scopeLabel = "global (unscoped servers only)"
			}
			return &mcp.Message{Error: &mcp.RPCError{
				Code:    -32601,
				Message: "Tool not available in " + scopeLabel + ": " + params.Name,
			}}
		}
		return s.engine.CallTool(ctx, params.Name, params.Arguments, s.sessionClient(sessID), workspace)

	default:
		return &mcp.Message{Error: &mcp.RPCError{Code: -32601, Message: "Method not found: " + msg.Method}}
	}
}

// formatHTTPClientName renders MCP clientInfo into a short display
// string. Mirrors the same logic the stdio proxy uses so dashboard
// attribution looks identical whether the client connects over stdio
// or HTTP.
func formatHTTPClientName(info mcp.Info) string {
	name := strings.TrimSpace(info.Name)
	if name == "" {
		return "unknown"
	}
	if info.Version != "" {
		return name + " " + info.Version
	}
	return name
}

// handleMCPGet streams server-initiated notifications to a connected
// client. The only message we emit unprompted today is
// notifications/tools/list_changed on proxy reload.
func (s *Server) handleMCPGet(w http.ResponseWriter, r *http.Request) {
	sessID := r.Header.Get("Mcp-Session-Id")
	if sessID == "" || !s.touchSession(sessID) {
		http.Error(w, "Mcp-Session-Id is required and must reference an active session.", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming responses unsupported by this connection.", http.StatusInternalServerError)
		return
	}

	sub, unsub := s.engine.SubscribeToolsChanged()
	defer unsub()
	shutdown := s.engine.ShuttingDown()

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-shutdown:
			// Daemon is going away. Tell the client so the stdio bridge
			// can exit cleanly and the MCP client sees "disconnected"
			// instead of staying in a stale connected state.
			msg := mcp.Message{JSONRPC: "2.0", Method: mcp.MethodAeolusShutdown}
			_ = writeSSE(w, msg)
			flusher.Flush()
			return
		case _, open := <-sub:
			if !open {
				return
			}
			msg := mcp.Message{JSONRPC: "2.0", Method: mcp.MethodToolsListChanged}
			if err := writeSSE(w, msg); err != nil {
				return
			}
			flusher.Flush()
		case <-heartbeat.C:
			if _, err := fmt.Fprintf(w, ": heartbeat\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (s *Server) handleMCPDelete(w http.ResponseWriter, r *http.Request) {
	sessID := r.Header.Get("Mcp-Session-Id")
	if sessID == "" {
		http.Error(w, "Mcp-Session-Id header required.", http.StatusBadRequest)
		return
	}
	s.closeSession(sessID)
	w.WriteHeader(http.StatusNoContent)
}

// --- session bookkeeping ---

func newSessionID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func (s *Server) openSession(id string) {
	s.mcpMu.Lock()
	s.mcpSessions[id] = &mcpSession{id: id, lastSeen: time.Now()}
	s.mcpMu.Unlock()
}

// setSessionClient records the MCP client identity for a session at
// initialize time. Subsequent tool calls on this session attribute back
// to this client in the dashboard.
func (s *Server) setSessionClient(id, client string) {
	s.mcpMu.Lock()
	defer s.mcpMu.Unlock()
	if sess, ok := s.mcpSessions[id]; ok {
		sess.clientName = client
	}
}

// sessionClient returns the client name recorded for a session, or
// "unknown" if none was captured (older clients, race conditions).
func (s *Server) sessionClient(id string) string {
	s.mcpMu.Lock()
	defer s.mcpMu.Unlock()
	if sess, ok := s.mcpSessions[id]; ok && sess.clientName != "" {
		return sess.clientName
	}
	return "unknown"
}

// setSessionWorkspace binds the resolved workspace to a session at
// initialize time.
func (s *Server) setSessionWorkspace(id, workspace string) {
	s.mcpMu.Lock()
	defer s.mcpMu.Unlock()
	if sess, ok := s.mcpSessions[id]; ok {
		sess.workspaceName = workspace
	}
}

// sessionWorkspace returns the workspace bound to a session. Empty
// string means "global" — only unscoped upstreams visible.
func (s *Server) sessionWorkspace(id string) string {
	s.mcpMu.Lock()
	defer s.mcpMu.Unlock()
	if sess, ok := s.mcpSessions[id]; ok {
		return sess.workspaceName
	}
	return ""
}

// filterToolsByScope drops tools whose owning upstream isn't visible
// in the current scope. The scope is:
//   - workspace == ""      → only unscoped upstreams (not in any workspace)
//   - workspace valid      → only that workspace's Include list
//   - workspace unknown    → no filter (permissive)
//
// Upstream name is the part of the exposed tool name before the first
// ".". Tools without a "." prefix (shouldn't happen for upstream
// tools, but guard anyway) pass through unfiltered.
func (s *Server) filterToolsByScope(tools []mcp.Tool, workspace string) []mcp.Tool {
	visible, applyFilter := s.scopeVisibleUpstreams(workspace)
	if !applyFilter {
		return tools
	}
	set := make(map[string]struct{}, len(visible))
	for _, n := range visible {
		set[n] = struct{}{}
	}
	out := make([]mcp.Tool, 0, len(tools))
	for _, t := range tools {
		i := strings.IndexByte(t.Name, '.')
		if i <= 0 {
			continue // skip malformed; never expose
		}
		if _, ok := set[t.Name[:i]]; ok {
			out = append(out, t)
		}
	}
	return out
}

// toolAllowedInScope gates tools/call. True when the tool's upstream
// is visible in the current scope (or the scope is permissive).
func (s *Server) toolAllowedInScope(exposedName, workspace string) bool {
	visible, applyFilter := s.scopeVisibleUpstreams(workspace)
	if !applyFilter {
		return true
	}
	i := strings.IndexByte(exposedName, '.')
	if i <= 0 {
		return false
	}
	upstream := exposedName[:i]
	for _, n := range visible {
		if n == upstream {
			return true
		}
	}
	return false
}

// scopeVisibleUpstreams resolves a workspace name to the set of
// upstream names a session in that scope is allowed to see. The second
// return is false when the caller should not filter at all (unknown
// workspace — permissive fallback so a broken config doesn't surface
// zero tools).
func (s *Server) scopeVisibleUpstreams(workspace string) ([]string, bool) {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	if s.cfg == nil {
		return nil, false
	}
	if workspace == "" {
		return s.cfg.UnscopedUpstreams(), true
	}
	if s.cfg.WorkspaceByName(workspace) == nil {
		return nil, false
	}
	return s.cfg.VisibleUpstreams(workspace), true
}

// touchSession updates lastSeen and returns whether the session is still
// alive. Expired sessions (older than sessionIdleTimeout) are dropped
// and reported as missing.
func (s *Server) touchSession(id string) bool {
	s.mcpMu.Lock()
	defer s.mcpMu.Unlock()
	sess, ok := s.mcpSessions[id]
	if !ok {
		return false
	}
	if time.Since(sess.lastSeen) > sessionIdleTimeout {
		delete(s.mcpSessions, id)
		return false
	}
	sess.lastSeen = time.Now()
	return true
}

func (s *Server) closeSession(id string) {
	s.mcpMu.Lock()
	delete(s.mcpSessions, id)
	s.mcpMu.Unlock()
}
