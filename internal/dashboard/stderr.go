package dashboard

import (
	"encoding/json"
	"net/http"
	"sync"
)

// stderrStore keeps a small per-upstream ring of the most recent
// stderr lines. The cap is small on purpose — this is for diagnostic
// surfacing in the dashboard, not a full log. Long-form upstream
// stderr still goes to ~/Library/Logs/aeolus/aeolus.log via the
// daemon's forward.
type stderrStore struct {
	mu    sync.Mutex
	lines map[string][]string
	cap   int
}

const defaultStderrCap = 200

func newStderrStore() *stderrStore {
	return &stderrStore{
		lines: map[string][]string{},
		cap:   defaultStderrCap,
	}
}

// Append adds one line to the named upstream's ring. Trims to cap.
// Safe to call concurrently.
func (s *stderrStore) Append(upstream, line string) {
	if s == nil || upstream == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	buf := s.lines[upstream]
	buf = append(buf, line)
	if len(buf) > s.cap {
		buf = buf[len(buf)-s.cap:]
	}
	s.lines[upstream] = buf
}

// Lines returns the current buffer (copy) for an upstream. Empty
// slice if the upstream has produced no stderr yet.
func (s *stderrStore) Lines(upstream string) []string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	buf := s.lines[upstream]
	if len(buf) == 0 {
		return []string{}
	}
	out := make([]string, len(buf))
	copy(out, buf)
	return out
}

// Forget drops the buffer for an upstream. Called when an upstream is
// removed from config so stale lines from a deleted server don't
// linger.
func (s *stderrStore) Forget(upstream string) {
	if s == nil || upstream == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.lines, upstream)
}

// AppendUpstreamStderr is the public hook for main.go's stderr
// forwarder. Each line the daemon receives from an upstream's stderr
// pipe gets a copy here so the dashboard can show it in the broken-
// upstream UI.
func (s *Server) AppendUpstreamStderr(upstream, line string) {
	if s == nil {
		return
	}
	s.stderr.Append(upstream, line)
}

func (s *Server) handleUpstreamStderr(w http.ResponseWriter, r *http.Request, name string) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"lines": s.stderr.Lines(name),
	})
}
