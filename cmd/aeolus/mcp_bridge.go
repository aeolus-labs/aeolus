package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// handleMCPBridge runs the stdio↔HTTP bridge. MCP clients that don't yet
// speak Streamable HTTP (or that the user wants to launch with a plain
// command path) spawn `aeolus mcp` as their MCP server. The bridge does
// two things at once:
//
//   - Forwards line-delimited JSON-RPC from stdin to the daemon's /mcp
//     endpoint (one POST per request) and writes the response to stdout.
//   - Once a session id is established, holds a long-lived GET open to
//     /mcp and pipes every server-initiated notification (especially
//     `notifications/tools/list_changed`) back to stdout so the client
//     can refresh without reconnecting.
func handleMCPBridge(args []string) {
	fs := flag.NewFlagSet("mcp", flag.ExitOnError)
	daemonURL := fs.String("daemon", "http://localhost:8765/mcp", "URL of the running aeolus daemon's MCP endpoint")
	timeout := fs.Duration("timeout", 5*time.Minute, "per-request timeout")
	_ = fs.Parse(args)

	postClient := &http.Client{Timeout: *timeout}
	sseClient := &http.Client{} // long-lived, no timeout

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var (
		sessMu     sync.Mutex
		sessID     string
		sseStarted bool

		outMu sync.Mutex
	)

	writeOut := func(data []byte) error {
		outMu.Lock()
		defer outMu.Unlock()
		return writeNDJSON(os.Stdout, data)
	}

	stdin := bufio.NewReaderSize(os.Stdin, 1<<20)

	for {
		line, err := readNDJSONLine(stdin)
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "aeolus mcp: read stdin:", err)
			os.Exit(1)
		}
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, *daemonURL, bytes.NewReader(line))
		if err != nil {
			fmt.Fprintln(os.Stderr, "aeolus mcp: build request:", err)
			os.Exit(1)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")

		sessMu.Lock()
		if sessID != "" {
			req.Header.Set("Mcp-Session-Id", sessID)
		}
		sessMu.Unlock()

		resp, err := postClient.Do(req)
		if err != nil {
			fmt.Fprintln(os.Stderr, "aeolus mcp: post to daemon:", err)
			os.Exit(1)
		}

		// Capture the session id from the initialize response and, the
		// first time we see one, kick off the SSE listener.
		if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
			sessMu.Lock()
			sessID = sid
			start := !sseStarted
			sseStarted = true
			sessMu.Unlock()
			if start {
				go runSSE(ctx, sseClient, *daemonURL, sid, writeOut)
			}
		}

		switch {
		case resp.StatusCode == http.StatusAccepted:
			// Notification acknowledged; no body to forward.
			_ = resp.Body.Close()
		case resp.StatusCode >= 200 && resp.StatusCode < 300:
			body, readErr := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
			_ = resp.Body.Close()
			if readErr != nil {
				fmt.Fprintln(os.Stderr, "aeolus mcp: read response:", readErr)
				os.Exit(1)
			}
			if err := writeOut(body); err != nil {
				fmt.Fprintln(os.Stderr, "aeolus mcp: write stdout:", err)
				os.Exit(1)
			}
		default:
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
			_ = resp.Body.Close()
			fmt.Fprintf(os.Stderr,
				"aeolus mcp: daemon responded %s: %s\n",
				resp.Status, bytes.TrimSpace(body),
			)
			if id := extractID(line); id != nil {
				rpcErr := mcpErrorPayload(id, -32000, fmt.Sprintf("Aeolus daemon error: %s", resp.Status))
				_ = writeOut(rpcErr)
			}
		}
	}
}

// runSSE holds a long-lived GET open to the daemon's MCP endpoint and
// forwards each server-initiated JSON-RPC notification it receives to
// stdout as NDJSON. The stream gets reconnected with exponential backoff
// if the daemon restarts or the connection drops.
func runSSE(ctx context.Context, client *http.Client, daemonURL, sessID string, writeOut func([]byte) error) {
	backoff := time.Second
	for {
		err := streamSSE(ctx, client, daemonURL, sessID, writeOut)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "aeolus mcp: notification stream:", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

// streamSSE opens GET /mcp and parses the daemon's SSE event stream,
// writing each `data:` payload to stdout via writeOut. Returns when the
// stream ends or the context is cancelled.
//
// If the daemon emits the `notifications/aeolus/shutdown` notification,
// the bridge calls os.Exit(0) — that closes stdio to the parent MCP
// client (Claude Code, Cursor, etc.) so the client immediately marks
// the server as disconnected instead of holding a stale connection.
func streamSSE(ctx context.Context, client *http.Client, daemonURL, sessID string, writeOut func([]byte) error) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, daemonURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Mcp-Session-Id", sessID)

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("daemon %s", resp.Status)
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64<<10), 4<<20)

	var data strings.Builder
	flush := func() {
		if data.Len() == 0 {
			return
		}
		payload := strings.TrimRight(data.String(), "\n")
		data.Reset()
		if payload == "" {
			return
		}
		if isShutdownNotification([]byte(payload)) {
			// Daemon is going away. Exit cleanly so stdio closes and
			// our parent MCP client immediately knows we're gone.
			os.Exit(0)
		}
		if err := writeOut([]byte(payload)); err != nil {
			fmt.Fprintln(os.Stderr, "aeolus mcp: write notification:", err)
		}
	}

	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, ":"):
			// Comment / heartbeat line — ignore.
		case strings.HasPrefix(line, "data:"):
			data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
			data.WriteByte('\n')
		default:
			// Other SSE fields (event:, id:, retry:) — ignore.
		}
	}
	return scanner.Err()
}

// isShutdownNotification returns true if the SSE payload is the
// daemon's notifications/aeolus/shutdown signal.
func isShutdownNotification(payload []byte) bool {
	var probe struct {
		Method string `json:"method"`
	}
	if err := json.Unmarshal(payload, &probe); err != nil {
		return false
	}
	return probe.Method == "notifications/aeolus/shutdown"
}

// readNDJSONLine reads one JSON object terminated by '\n' from r.
func readNDJSONLine(r *bufio.Reader) ([]byte, error) {
	return r.ReadBytes('\n')
}

// writeNDJSON writes data to w, ensuring it ends with exactly one '\n'.
func writeNDJSON(w io.Writer, data []byte) error {
	data = bytes.TrimRight(data, "\n")
	if _, err := w.Write(data); err != nil {
		return err
	}
	_, err := w.Write([]byte{'\n'})
	return err
}

// extractID pulls the JSON-RPC `id` field out of a raw message body so the
// bridge can fabricate a matching error response when the daemon refuses.
func extractID(body []byte) json.RawMessage {
	var probe struct {
		ID json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return nil
	}
	if len(probe.ID) == 0 {
		return nil
	}
	return probe.ID
}

func mcpErrorPayload(id json.RawMessage, code int, message string) []byte {
	type rpcErr struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	payload := struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Error   rpcErr          `json:"error"`
	}{"2.0", id, rpcErr{code, message}}
	b, _ := json.Marshal(payload)
	return b
}
