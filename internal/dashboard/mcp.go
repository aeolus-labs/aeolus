package dashboard

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
		w.Header().Set("Mcp-Session-Id", sessID)
	} else if !s.touchSession(sessID) {
		http.Error(w, "Unknown or expired session — start a new one with an initialize call.", http.StatusBadRequest)
		return
	}

	if msg.IsNotification() {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	resp := s.routeMCPRequest(r.Context(), &msg)
	resp.JSONRPC = "2.0"
	resp.ID = msg.ID

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// routeMCPRequest dispatches an MCP request to the engine and returns the
// response. The returned message has no JSONRPC/ID set; the caller fills
// those in.
func (s *Server) routeMCPRequest(ctx context.Context, msg *mcp.Message) *mcp.Message {
	switch msg.Method {
	case mcp.MethodInitialize:
		var params mcp.InitializeParams
		if len(msg.Params) > 0 {
			_ = json.Unmarshal(msg.Params, &params)
		}
		result := s.engine.HandleInitialize(params.ClientInfo)
		b, _ := json.Marshal(result)
		return &mcp.Message{Result: b}

	case mcp.MethodToolsList:
		tools := s.engine.ListTools()
		b, _ := json.Marshal(mcp.ToolsListResult{Tools: tools})
		return &mcp.Message{Result: b}

	case mcp.MethodToolsCall:
		var params mcp.ToolsCallParams
		if len(msg.Params) > 0 {
			if err := json.Unmarshal(msg.Params, &params); err != nil {
				return &mcp.Message{Error: &mcp.RPCError{Code: -32602, Message: "Invalid params: " + err.Error()}}
			}
		}
		return s.engine.CallTool(ctx, params.Name, params.Arguments)

	default:
		return &mcp.Message{Error: &mcp.RPCError{Code: -32601, Message: "Method not found: " + msg.Method}}
	}
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

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
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
