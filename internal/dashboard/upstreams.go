package dashboard

import (
	"net/http"
	"strings"
)

// handleUpstream multiplexes /api/upstreams/{name}/... endpoints. Today
// just /reconnect lives here; future additions (per-upstream tool toggle,
// pause/resume, workspace membership) will slot in beside it.
func (s *Server) handleUpstream(w http.ResponseWriter, r *http.Request) {
	// Strip the /api/upstreams/ prefix and split into name + verb.
	rest := strings.TrimPrefix(r.URL.Path, "/api/upstreams/")
	if rest == "" {
		http.NotFound(w, r)
		return
	}
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) < 2 {
		http.NotFound(w, r)
		return
	}
	name, verb := parts[0], parts[1]
	if name == "" || verb == "" {
		http.NotFound(w, r)
		return
	}

	switch verb {
	case "reconnect":
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}
		s.handleUpstreamReconnect(w, r, name)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) handleUpstreamReconnect(w http.ResponseWriter, r *http.Request, name string) {
	s.cfgMu.RLock()
	fn := s.reconnectFn
	cfg := s.cfg
	s.cfgMu.RUnlock()

	if fn == nil {
		http.Error(w, "reconnect is not enabled in this build", http.StatusServiceUnavailable)
		return
	}
	// Sanity check: the named upstream actually exists in current config.
	// Saves users from typos in the URL and gives a clearer error.
	found := false
	if cfg != nil {
		for _, u := range cfg.Upstreams {
			if u.Name == name {
				found = true
				break
			}
		}
	}
	if !found {
		http.Error(w, "Unknown upstream: "+name, http.StatusNotFound)
		return
	}

	if err := fn(r.Context(), name); err != nil {
		http.Error(w, "Reconnect failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
