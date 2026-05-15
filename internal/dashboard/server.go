// Package dashboard hosts the Aeolus dashboard: an HTTP server that serves
// the embedded React app and streams tool-call events over SSE.
package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"
)

// Event is a single tool call observed by the proxy.
type Event struct {
	Time      time.Time `json:"time"`
	Upstream  string    `json:"upstream"`
	Tool      string    `json:"tool"`
	LatencyMs int64     `json:"latency_ms"`
	Status    string    `json:"status"`
}

type Server struct {
	addr string
	log  *slog.Logger

	mu          sync.Mutex
	subscribers map[chan Event]struct{}
	recent      []Event

	maxRecent int
	subBuffer int
}

func New(addr string, log *slog.Logger) *Server {
	return &Server{
		addr:        addr,
		log:         log,
		subscribers: make(map[chan Event]struct{}),
		recent:      make([]Event, 0, 256),
		maxRecent:   256,
		subBuffer:   32,
	}
}

// Emit records an event in the ring buffer and broadcasts it to subscribers.
// Slow subscribers see the event dropped.
func (s *Server) Emit(e Event) {
	s.mu.Lock()
	s.recent = append(s.recent, e)
	if len(s.recent) > s.maxRecent {
		s.recent = s.recent[len(s.recent)-s.maxRecent:]
	}
	subs := make([]chan Event, 0, len(s.subscribers))
	for ch := range s.subscribers {
		subs = append(subs, ch)
	}
	s.mu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- e:
		default:
		}
	}
}

// Run starts the HTTP server. Returns when ctx is canceled or the listener fails.
func (s *Server) Run(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/events", s.handleEvents)
	mux.HandleFunc("/api/stats", s.handleStats)
	if assets != nil {
		mux.Handle("/", http.FileServer(http.FS(assets)))
	}

	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("dashboard listen %s: %w", s.addr, err)
	}
	s.addr = ln.Addr().String()
	s.log.Info("dashboard_listening", "url", "http://"+s.addr)

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		return nil
	case err := <-errCh:
		return err
	}
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	ch := make(chan Event, s.subBuffer)
	s.mu.Lock()
	s.subscribers[ch] = struct{}{}
	recent := make([]Event, len(s.recent))
	copy(recent, s.recent)
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.subscribers, ch)
		s.mu.Unlock()
	}()

	for _, e := range recent {
		if err := writeSSE(w, e); err != nil {
			return
		}
	}
	flusher.Flush()

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case e := <-ch:
			if err := writeSSE(w, e); err != nil {
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

func writeSSE(w io.Writer, e Event) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "data: %s\n\n", b)
	return err
}

type statsResponse struct {
	Upstreams []string `json:"upstreams"`
	Total     int      `json:"total"`
	Errors    int      `json:"errors"`
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	upstreams := make(map[string]struct{})
	errs := 0
	for _, e := range s.recent {
		upstreams[e.Upstream] = struct{}{}
		if e.Status == "error" {
			errs++
		}
	}
	total := len(s.recent)
	s.mu.Unlock()

	ups := make([]string, 0, len(upstreams))
	for u := range upstreams {
		ups = append(ups, u)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(statsResponse{
		Upstreams: ups,
		Total:     total,
		Errors:    errs,
	})
}
