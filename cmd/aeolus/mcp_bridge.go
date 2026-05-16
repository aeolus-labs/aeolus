package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"
)

// handleMCPBridge runs the stdio↔HTTP bridge. MCP clients that don't yet
// speak Streamable HTTP (or that the user wants to launch with a plain
// command path) spawn `aeolus mcp` as their MCP server. This bridge
// forwards line-delimited JSON-RPC from stdin to the running daemon's
// /mcp endpoint and writes responses back to stdout.
//
// Today the bridge is one POST per request. A future revision can also
// hold a GET open to the daemon to forward server-initiated
// notifications (tools/list_changed, etc.) back to the client.
func handleMCPBridge(args []string) {
	fs := flag.NewFlagSet("mcp", flag.ExitOnError)
	daemonURL := fs.String("daemon", "http://localhost:8765/mcp", "URL of the running aeolus daemon's MCP endpoint")
	timeout := fs.Duration("timeout", 5*time.Minute, "per-request timeout")
	_ = fs.Parse(args)

	client := &http.Client{Timeout: *timeout}

	var (
		sessMu sync.Mutex
		sessID string
	)

	stdin := bufio.NewReaderSize(os.Stdin, 1<<20)
	out := os.Stdout

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

		req, err := http.NewRequest(http.MethodPost, *daemonURL, bytes.NewReader(line))
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

		resp, err := client.Do(req)
		if err != nil {
			fmt.Fprintln(os.Stderr, "aeolus mcp: post to daemon:", err)
			os.Exit(1)
		}

		if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
			sessMu.Lock()
			sessID = sid
			sessMu.Unlock()
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
			if err := writeNDJSON(out, body); err != nil {
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
			// Try to surface an MCP-style error response to the client so the
			// MCP client doesn't hang. Best-effort.
			if id := extractID(line); id != nil {
				rpcErr := mcpErrorPayload(id, -32000, fmt.Sprintf("Aeolus daemon error: %s", resp.Status))
				_ = writeNDJSON(out, rpcErr)
			}
		}
	}
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
