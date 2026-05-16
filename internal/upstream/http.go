package upstream

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aeolus-labs/aeolus/internal/mcp"
)

// httpUpstream is the Streamable HTTP Server implementation per the MCP
// transport spec. The current version handles synchronous JSON responses
// and SSE-framed responses to a single POST; server-initiated notifications
// over a long-lived GET stream are not yet wired up (TODO).
type httpUpstream struct {
	name     string
	endpoint string
	headers  map[string]string
	client   *http.Client
	log      *slog.Logger

	nextID    atomic.Int64
	sessionID atomic.Value // string

	notif chan *mcp.Message

	closeMu sync.Mutex
	closed  bool
}

// StartHTTP creates an HTTP-backed upstream pointing at endpoint.
// headers is optional — typically Authorization, X-Api-Key, etc.
// Keychain references inside header values are resolved at construction.
func StartHTTP(_ context.Context, name, endpoint string, headers map[string]string, log *slog.Logger) (*httpUpstream, error) {
	if endpoint == "" {
		return nil, fmt.Errorf("upstream %s: http url required", name)
	}
	resolved, err := resolveHeaders(headers)
	if err != nil {
		return nil, fmt.Errorf("upstream %s: %w", name, err)
	}
	u := &httpUpstream{
		name:     name,
		endpoint: endpoint,
		headers:  resolved,
		client:   &http.Client{Timeout: 60 * time.Second},
		log:      log,
		notif:    make(chan *mcp.Message, 16),
	}
	return u, nil
}

func resolveHeaders(in map[string]string) (map[string]string, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		if !strings.HasPrefix(v, keychainPrefix) {
			out[k] = v
			continue
		}
		fakeEnv := []string{"_=" + v}
		resolved, err := resolveEnv(fakeEnv)
		if err != nil {
			return nil, err
		}
		out[k] = strings.TrimPrefix(resolved[0], "_=")
	}
	return out, nil
}

func (u *httpUpstream) Name() string                       { return u.name }
func (u *httpUpstream) Stderr() io.Reader                  { return nil }
func (u *httpUpstream) Notifications() <-chan *mcp.Message { return u.notif }

func (u *httpUpstream) Shutdown(_ time.Duration) {
	u.closeMu.Lock()
	defer u.closeMu.Unlock()
	if u.closed {
		return
	}
	u.closed = true
	close(u.notif)
}

func (u *httpUpstream) Initialize(ctx context.Context, clientInfo mcp.Info) (*mcp.InitializeResult, error) {
	resp, err := u.Request(ctx, mcp.MethodInitialize, mcp.InitializeParams{
		ProtocolVersion: mcp.ProtocolVersion,
		Capabilities:    json.RawMessage(`{}`),
		ClientInfo:      clientInfo,
	})
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

// Request POSTs a JSON-RPC request to the endpoint and returns the matching
// response, regardless of whether the server replies with application/json
// or text/event-stream.
func (u *httpUpstream) Request(ctx context.Context, method string, params any) (*mcp.Message, error) {
	id := u.nextID.Add(1)
	idRaw, _ := json.Marshal(id)
	body, err := u.encodeMessage(idRaw, method, params)
	if err != nil {
		return nil, err
	}

	httpResp, err := u.do(ctx, body)
	if err != nil {
		return nil, err
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode == http.StatusAccepted {
		return nil, fmt.Errorf("server returned 202 to a request (notifications-only response)")
	}
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(httpResp.Body, 1024))
		return nil, fmt.Errorf("http %s: %s", httpResp.Status, strings.TrimSpace(string(b)))
	}

	ct := httpResp.Header.Get("Content-Type")
	switch {
	case strings.HasPrefix(ct, "application/json"):
		var msg mcp.Message
		if err := json.NewDecoder(httpResp.Body).Decode(&msg); err != nil {
			return nil, fmt.Errorf("decode JSON response: %w", err)
		}
		return &msg, nil
	case strings.HasPrefix(ct, "text/event-stream"):
		return u.readSSEResponse(httpResp.Body, id)
	default:
		return nil, fmt.Errorf("unexpected Content-Type %q on response", ct)
	}
}

// Notify POSTs a JSON-RPC notification (no ID). Drains and discards the
// response body; servers should reply 202 Accepted.
func (u *httpUpstream) Notify(method string, params any) error {
	body, err := u.encodeMessage(nil, method, params)
	if err != nil {
		return err
	}
	httpResp, err := u.do(context.Background(), body)
	if err != nil {
		return err
	}
	defer httpResp.Body.Close()
	if httpResp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(httpResp.Body, 1024))
		return fmt.Errorf("notify %s: http %s: %s", method, httpResp.Status, strings.TrimSpace(string(b)))
	}
	_, _ = io.Copy(io.Discard, httpResp.Body)
	return nil
}

func (u *httpUpstream) encodeMessage(idRaw json.RawMessage, method string, params any) ([]byte, error) {
	var paramsRaw json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return nil, err
		}
		paramsRaw = b
	}
	msg := &mcp.Message{
		JSONRPC: "2.0",
		ID:      idRaw,
		Method:  method,
		Params:  paramsRaw,
	}
	return json.Marshal(msg)
}

func (u *httpUpstream) do(ctx context.Context, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range u.headers {
		req.Header.Set(k, v)
	}
	if sid, ok := u.sessionID.Load().(string); ok && sid != "" {
		req.Header.Set("Mcp-Session-Id", sid)
	}
	resp, err := u.client.Do(req)
	if err != nil {
		return nil, err
	}
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		u.sessionID.Store(sid)
	}
	return resp, nil
}

// readSSEResponse parses an SSE stream until it finds a JSON-RPC response
// whose ID matches wantID. Any other notifications encountered along the
// way are forwarded to the notif channel.
func (u *httpUpstream) readSSEResponse(body io.Reader, wantID int64) (*mcp.Message, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var dataLines []string
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			// Event boundary — process accumulated data lines.
			if len(dataLines) == 0 {
				continue
			}
			data := strings.Join(dataLines, "\n")
			dataLines = dataLines[:0]
			var msg mcp.Message
			if err := json.Unmarshal([]byte(data), &msg); err != nil {
				continue // ignore malformed events
			}
			if msg.IsResponse() {
				if id, ok := parseID(msg.ID); ok && id == wantID {
					return &msg, nil
				}
			}
			if msg.IsNotification() {
				select {
				case u.notif <- &msg:
				default:
					u.log.Warn("upstream_notification_dropped", "upstream", u.name, "method", msg.Method)
				}
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue // SSE comment
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
		// other SSE fields (event:, id:, retry:) ignored
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read SSE: %w", err)
	}
	return nil, fmt.Errorf("SSE stream ended without a matching response for id %d", wantID)
}
