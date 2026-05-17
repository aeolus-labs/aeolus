package dashboard

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
)

// DashboardState is the persisted UI state for the dashboard. Lives in
// a separate file from aeolus.yaml on purpose — anything in this file
// is ephemeral UI preference, not user-managed config. Keep it small
// and only add fields that make sense to persist across browser
// restarts (e.g., dismissed tour, sidebar collapsed, last-selected
// workspace). Anything user-visible in the YAML belongs in aeolus.yaml.
type DashboardState struct {
	TourDismissed    bool `json:"tour_dismissed,omitempty"`
	SidebarCollapsed bool `json:"sidebar_collapsed,omitempty"`
}

// stateStore owns the JSON file on disk and serializes reads/writes.
// Atomic write via tmp+rename so a crash mid-save can't leave the
// file half-written.
type stateStore struct {
	path string
	mu   sync.Mutex
	cur  DashboardState
}

func openStateStore(path string) (*stateStore, error) {
	s := &stateStore{path: path}
	if path == "" {
		return s, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create state dir: %w", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return s, nil
		}
		return nil, fmt.Errorf("read state: %w", err)
	}
	if err := json.Unmarshal(data, &s.cur); err != nil {
		// Corrupt file? Don't fail — log on the caller's side and
		// start fresh. Worst case the user re-dismisses the tour.
		return s, nil
	}
	return s, nil
}

func (s *stateStore) Get() DashboardState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur
}

// Set replaces the entire state and writes to disk. The frontend
// sends the complete state on every PUT, so merging server-side isn't
// needed.
func (s *stateStore) Set(next DashboardState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cur = next
	if s.path == "" {
		return nil
	}
	data, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// handleDashboardState serves GET /api/dashboard/state and
// PUT /api/dashboard/state. Returns 503 when persistence isn't enabled
// — the frontend treats that as "use defaults, don't persist."
func (s *Server) handleDashboardState(w http.ResponseWriter, r *http.Request) {
	if s.state == nil {
		http.Error(w, "dashboard state persistence is not enabled", http.StatusServiceUnavailable)
		return
	}
	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(s.state.Get())
	case http.MethodPut:
		var next DashboardState
		if err := json.NewDecoder(r.Body).Decode(&next); err != nil {
			http.Error(w, "Couldn't parse state JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := s.state.Set(next); err != nil {
			http.Error(w, "Couldn't save state: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(s.state.Get())
	default:
		http.Error(w, "GET or PUT only", http.StatusMethodNotAllowed)
	}
}
