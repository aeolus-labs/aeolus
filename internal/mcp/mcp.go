// Package mcp contains the subset of the Model Context Protocol that
// Aeolus needs to proxy: JSON-RPC 2.0 envelopes and a few well-known
// method names. Everything else is passed through as opaque JSON.
package mcp

import "encoding/json"

// Message is a JSON-RPC 2.0 envelope. A Message is one of:
//   - a request       (ID + Method)
//   - a notification  (Method only, no ID)
//   - a response      (ID + Result)
//   - an error        (ID + Error)
type Message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (m *Message) IsRequest() bool      { return len(m.ID) > 0 && m.Method != "" }
func (m *Message) IsResponse() bool     { return len(m.ID) > 0 && m.Method == "" }
func (m *Message) IsNotification() bool { return len(m.ID) == 0 && m.Method != "" }

const (
	MethodInitialize       = "initialize"
	MethodInitialized      = "notifications/initialized"
	MethodToolsList        = "tools/list"
	MethodToolsCall        = "tools/call"
	MethodToolsListChanged = "notifications/tools/list_changed"

	ProtocolVersion = "2024-11-05"
)

type Info struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

type InitializeParams struct {
	ProtocolVersion string          `json:"protocolVersion"`
	Capabilities    json.RawMessage `json:"capabilities,omitempty"`
	ClientInfo      Info            `json:"clientInfo"`
}

type InitializeResult struct {
	ProtocolVersion string          `json:"protocolVersion"`
	Capabilities    json.RawMessage `json:"capabilities,omitempty"`
	ServerInfo      Info            `json:"serverInfo"`
	Instructions    string          `json:"instructions,omitempty"`
}

// Tool is one entry of a tools/list response. Aeolus only needs Name for
// filtering; the rest is preserved verbatim.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
}

// ToolsListResult is the result payload of a tools/list response.
type ToolsListResult struct {
	Tools      []Tool          `json:"tools"`
	NextCursor json.RawMessage `json:"nextCursor,omitempty"`
}

// ToolsCallParams is the params payload of a tools/call request.
type ToolsCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}
