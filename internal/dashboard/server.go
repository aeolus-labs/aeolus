// Package dashboard hosts the Aeolus dashboard: an HTTP server that serves
// the embedded React app and streams tool-call events over SSE.
package dashboard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aeolus-labs/aeolus/internal/config"
	"github.com/aeolus-labs/aeolus/internal/mcp"
	"github.com/aeolus-labs/aeolus/internal/secrets"
	"github.com/aeolus-labs/aeolus/internal/upstream"
	"gopkg.in/yaml.v3"
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

	cfgMu       sync.RWMutex
	cfg         *config.Config
	configPath  string
	reloadFn    ReloadFunc // nil until SetReloader is called

	catalogMu          sync.RWMutex
	catalog            []CatalogEntry
	catalogSubscribers map[chan CatalogBatch]struct{}
	catalogLoading     atomic.Bool

	maxRecent int
	subBuffer int
}

// ReloadFunc is invoked after a config change is persisted. It receives the
// new validated config and is responsible for re-initializing the proxy.
type ReloadFunc func(ctx context.Context, cfg *config.Config) error

// CatalogBatch is a chunk of catalog entries emitted by /api/catalog/stream.
// done == true on the final batch of a refresh cycle.
type CatalogBatch struct {
	Batch []CatalogEntry `json:"batch"`
	Done  bool           `json:"done"`
}

func New(addr string, log *slog.Logger, cfg *config.Config) *Server {
	s := &Server{
		addr:               addr,
		log:                log,
		cfg:                cfg,
		subscribers:        make(map[chan Event]struct{}),
		recent:             make([]Event, 0, 256),
		catalog:            nil, // populated by background registry fetch
		catalogSubscribers: make(map[chan CatalogBatch]struct{}),
		maxRecent:          256,
		subBuffer:          32,
	}
	return s
}

// SetConfig replaces the current config snapshot served by /api/config.
func (s *Server) SetConfig(cfg *config.Config) {
	s.cfgMu.Lock()
	s.cfg = cfg
	s.cfgMu.Unlock()
}

// SetReloader wires the dashboard to a function that can re-initialize the
// proxy with a new config, plus the path on disk where edits are persisted.
// PUT /api/config returns an error if no reloader is set.
func (s *Server) SetReloader(configPath string, fn ReloadFunc) {
	s.cfgMu.Lock()
	s.configPath = configPath
	s.reloadFn = fn
	s.cfgMu.Unlock()
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
	go s.refreshCatalogLoop(ctx)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/events", s.handleEvents)
	mux.HandleFunc("/api/stats", s.handleStats)
	mux.HandleFunc("/api/config", s.handleConfig)
	mux.HandleFunc("/api/catalog", s.handleCatalog)
	mux.HandleFunc("/api/catalog/stream", s.handleCatalogStream)
	mux.HandleFunc("/api/probe", s.handleProbe)
	mux.HandleFunc("/api/secrets/", s.handleSecret)
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

func writeSSE(w io.Writer, payload any) error {
	b, err := json.Marshal(payload)
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

// secretMask is the wire-sentinel returned in GET /api/config for env
// values. On PUT, any env value equal to secretMask is replaced with the
// previously-stored value, so editing a masked field preserves the secret.
const secretMask = "***"

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.cfgMu.RLock()
		cfg := s.cfg
		s.cfgMu.RUnlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(maskedConfig(cfg))
	case http.MethodPut:
		s.handleConfigPut(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// maskedConfig returns a copy of cfg with plaintext env values replaced by
// secretMask. keychain:* references stay visible — they're pointers, not
// secrets — so the UI can show that a value is keychain-backed.
func maskedConfig(cfg *config.Config) *config.Config {
	if cfg == nil {
		return nil
	}
	out := *cfg
	out.Upstreams = make([]config.Upstream, len(cfg.Upstreams))
	for i, u := range cfg.Upstreams {
		c := u
		if len(u.Env) > 0 {
			c.Env = make([]string, len(u.Env))
			for j, e := range u.Env {
				eq := strings.Index(e, "=")
				if eq < 0 {
					c.Env[j] = e
					continue
				}
				value := e[eq+1:]
				if strings.HasPrefix(value, "keychain:") {
					c.Env[j] = e // keep reference visible
				} else {
					c.Env[j] = e[:eq+1] + secretMask
				}
			}
		}
		out.Upstreams[i] = c
	}
	return &out
}

// preserveMaskedEnv restores env values in newCfg from oldCfg whenever the
// new value is the wire sentinel. Match is by upstream name + env key.
func preserveMaskedEnv(newCfg, oldCfg *config.Config) {
	if oldCfg == nil {
		return
	}
	oldByName := make(map[string]map[string]string, len(oldCfg.Upstreams))
	for _, u := range oldCfg.Upstreams {
		kv := make(map[string]string, len(u.Env))
		for _, e := range u.Env {
			if eq := strings.Index(e, "="); eq >= 0 {
				kv[e[:eq]] = e[eq+1:]
			}
		}
		oldByName[u.Name] = kv
	}
	for i, u := range newCfg.Upstreams {
		oldEnv, ok := oldByName[u.Name]
		if !ok {
			continue
		}
		for j, e := range u.Env {
			eq := strings.Index(e, "=")
			if eq < 0 {
				continue
			}
			if e[eq+1:] == secretMask {
				if real, ok := oldEnv[e[:eq]]; ok {
					newCfg.Upstreams[i].Env[j] = e[:eq+1] + real
				}
			}
		}
	}
}

func (s *Server) handleConfigPut(w http.ResponseWriter, r *http.Request) {
	s.cfgMu.RLock()
	path := s.configPath
	reload := s.reloadFn
	s.cfgMu.RUnlock()
	if path == "" || reload == nil {
		http.Error(w, "config edits are not enabled in this build", http.StatusServiceUnavailable)
		return
	}

	var newCfg config.Config
	if err := json.NewDecoder(r.Body).Decode(&newCfg); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	// Merge in any preserved secrets before validating: masked env values in
	// the request body get filled from the current in-memory config.
	s.cfgMu.RLock()
	currentCfg := s.cfg
	s.cfgMu.RUnlock()
	preserveMaskedEnv(&newCfg, currentCfg)
	if err := config.Validate(&newCfg); err != nil {
		http.Error(w, "invalid config: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Clean up keychain entries that were referenced by the old config but
	// aren't referenced by the new one (upstream removed, env var dropped,
	// or value swapped to a different keychain ref / plaintext).
	s.cleanupOrphanedSecrets(currentCfg, &newCfg)

	yamlBytes, err := yaml.Marshal(&newCfg)
	if err != nil {
		http.Error(w, "yaml marshal: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := atomicWriteFile(path, yamlBytes); err != nil {
		http.Error(w, "write config: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if err := reload(r.Context(), &newCfg); err != nil {
		http.Error(w, "reload failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	s.cfgMu.Lock()
	s.cfg = &newCfg
	s.cfgMu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(&newCfg)
}

// atomicWriteFile writes data to a sibling temp file and renames it into
// place so a crash mid-write can't leave a half-written config.
func atomicWriteFile(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

type probeRequest struct {
	Transport string            `json:"transport,omitempty"`
	Command   string            `json:"command,omitempty"`
	Args      []string          `json:"args,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	URL       string            `json:"url,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
}

func (s *Server) handleProbe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req probeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	transport := req.Transport
	if transport == "" {
		transport = "stdio"
	}
	switch transport {
	case "stdio":
		if req.Command == "" {
			http.Error(w, "command is required for stdio probe", http.StatusBadRequest)
			return
		}
	case "http":
		if req.URL == "" {
			http.Error(w, "url is required for http probe", http.StatusBadRequest)
			return
		}
	default:
		http.Error(w, "unknown transport "+transport, http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	envSlice := make([]string, 0, len(req.Env))
	for k, v := range req.Env {
		envSlice = append(envSlice, k+"="+v)
	}

	cfg := config.Upstream{
		Name:      "probe",
		Transport: transport,
		Command:   req.Command,
		Args:      req.Args,
		Env:       envSlice,
		URL:       req.URL,
		Headers:   req.Headers,
	}
	u, err := upstream.New(ctx, cfg, s.log)
	if err != nil {
		http.Error(w, "spawn failed: "+err.Error(), http.StatusBadRequest)
		return
	}
	defer u.Shutdown(2 * time.Second)

	// Capture stdio upstream stderr so error responses can include the
	// underlying reason. HTTP upstreams return nil here; their errors come
	// back as HTTP response bodies inside the Initialize/Request paths.
	stderrBuf := &captureBuf{}
	if r := u.Stderr(); r != nil {
		go func() { _, _ = io.Copy(stderrBuf, r) }()
	}

	probeErr := func(prefix string, err error) {
		// Give stderr a beat to flush before we read it.
		time.Sleep(150 * time.Millisecond)
		msg := prefix + ": " + err.Error()
		if se := strings.TrimSpace(stderrBuf.String()); se != "" {
			msg += "\n\nupstream stderr:\n" + se
		}
		http.Error(w, msg, http.StatusBadRequest)
	}

	if _, err := u.Initialize(ctx, mcp.Info{Name: "aeolus-probe", Version: "0.3.0"}); err != nil {
		probeErr("initialize failed", err)
		return
	}
	resp, err := u.Request(ctx, mcp.MethodToolsList, struct{}{})
	if err != nil {
		probeErr("tools/list failed", err)
		return
	}
	if resp.Error != nil {
		http.Error(w, "upstream error: "+resp.Error.Message, http.StatusBadRequest)
		return
	}
	var result mcp.ToolsListResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		http.Error(w, "parse failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"tools": result.Tools})
}

// cleanupOrphanedSecrets deletes keychain entries that the old config
// referenced but the new config does not. Failures are logged but don't
// fail the config save — a leftover keychain entry is harmless.
func (s *Server) cleanupOrphanedSecrets(old, new *config.Config) {
	if old == nil {
		return
	}
	inUse := make(map[string]bool)
	collect := func(cfg *config.Config, m map[string]bool) {
		if cfg == nil {
			return
		}
		for _, u := range cfg.Upstreams {
			for _, e := range u.Env {
				eq := strings.Index(e, "=")
				if eq < 0 {
					continue
				}
				value := e[eq+1:]
				if strings.HasPrefix(value, "keychain:") {
					m[strings.TrimPrefix(value, "keychain:")] = true
				}
			}
		}
	}
	collect(new, inUse)

	oldRefs := make(map[string]bool)
	collect(old, oldRefs)

	for ref := range oldRefs {
		if inUse[ref] {
			continue
		}
		if err := secrets.Delete(ref); err != nil {
			s.log.Warn("secret_cleanup_failed", "ref", ref, "error", err.Error())
			continue
		}
		s.log.Info("secret_cleaned", "ref", ref)
	}
}

// handleSecret writes or removes a secret in the OS keychain. Routes:
//
//	POST   /api/secrets/<name>   body: {"value": "..."}   — store
//	DELETE /api/secrets/<name>                            — remove
//
// The stored secret is then referenced from aeolus.yaml as
// KEY=keychain:<name>; upstream.Start resolves it at spawn time.
func (s *Server) handleSecret(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/api/secrets/")
	if name == "" || strings.Contains(name, "/") {
		http.Error(w, "secret name required", http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodPost:
		var body struct {
			Value string `json:"value"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if body.Value == "" {
			http.Error(w, "value is required", http.StatusBadRequest)
			return
		}
		if err := secrets.Set(name, body.Value); err != nil {
			http.Error(w, "keychain write failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	case http.MethodDelete:
		if err := secrets.Delete(name); err != nil {
			http.Error(w, "keychain delete failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// captureBuf is a thread-safe bytes.Buffer for capturing subprocess stderr
// across goroutine boundaries.
type captureBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (c *captureBuf) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.Write(p)
}

func (c *captureBuf) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.String()
}

func (s *Server) handleCatalog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.catalogMu.RLock()
	cat := s.catalog
	s.catalogMu.RUnlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(cat)
}

// refreshCatalogLoop fetches the MCP registry on startup and every hour after,
// keeping the static catalog as a fallback if the network is unavailable.
func (s *Server) refreshCatalogLoop(ctx context.Context) {
	tick := time.NewTicker(time.Hour)
	defer tick.Stop()
	s.refreshCatalog(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			s.refreshCatalog(ctx)
		}
	}
}

// refreshCatalog walks the MCP registry one page at a time, appending each
// page's mapped entries to the cache and broadcasting them to live subscribers.
// A nil-batch with done=true is broadcast on completion.
func (s *Server) refreshCatalog(ctx context.Context) {
	if !s.catalogLoading.CompareAndSwap(false, true) {
		return // refresh already in progress
	}
	defer s.catalogLoading.Store(false)
	defer s.broadcastCatalog(nil, true)

	started := time.Now()
	client := &http.Client{Timeout: registryTimeout}
	cursor := ""
	pages := 0
	seen := make(map[string]bool)
	total := 0

	for {
		pages++
		page, err := fetchRegistryPage(ctx, client, cursor)
		if err != nil {
			s.log.Warn("catalog_page_failed", "page", pages, "error", err.Error())
			break
		}
		batch := make([]CatalogEntry, 0, 32)
		for _, e := range page.Servers {
			if !isLatest(e) {
				continue
			}
			ce, ok := mapEntry(e.Server)
			if !ok || seen[ce.ID] {
				continue
			}
			seen[ce.ID] = true
			batch = append(batch, ce)
		}
		if len(batch) > 0 {
			s.catalogMu.Lock()
			s.catalog = append(s.catalog, batch...)
			s.catalogMu.Unlock()
			s.broadcastCatalog(batch, false)
			total += len(batch)
		}
		if page.Metadata.NextCursor == "" {
			break
		}
		cursor = page.Metadata.NextCursor
		if pages > 200 {
			break
		}
	}
	s.log.Info("catalog_refreshed",
		"count", total,
		"pages", pages,
		"duration_ms", time.Since(started).Milliseconds(),
	)
}

func (s *Server) broadcastCatalog(batch []CatalogEntry, done bool) {
	s.catalogMu.RLock()
	subs := make([]chan CatalogBatch, 0, len(s.catalogSubscribers))
	for ch := range s.catalogSubscribers {
		subs = append(subs, ch)
	}
	s.catalogMu.RUnlock()
	msg := CatalogBatch{Batch: batch, Done: done}
	for _, ch := range subs {
		select {
		case ch <- msg:
		default:
		}
	}
}

func (s *Server) handleCatalogStream(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	ch := make(chan CatalogBatch, 16)
	s.catalogMu.Lock()
	s.catalogSubscribers[ch] = struct{}{}
	initial := append([]CatalogEntry(nil), s.catalog...)
	loading := s.catalogLoading.Load()
	s.catalogMu.Unlock()

	defer func() {
		s.catalogMu.Lock()
		delete(s.catalogSubscribers, ch)
		s.catalogMu.Unlock()
	}()

	if err := writeSSE(w, CatalogBatch{Batch: initial, Done: !loading}); err != nil {
		return
	}
	flusher.Flush()

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case msg := <-ch:
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
