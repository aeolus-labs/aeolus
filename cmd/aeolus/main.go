package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/aeolus-labs/aeolus/internal/config"
	"github.com/aeolus-labs/aeolus/internal/dashboard"
	"github.com/aeolus-labs/aeolus/internal/proxy"
	"github.com/aeolus-labs/aeolus/internal/upstream"
	"github.com/aeolus-labs/aeolus/internal/watcher"
)

const shutdownTimeout = 1500 * time.Millisecond

// Build-injected version metadata. Defaults are for non-release builds
// (e.g. `go build` directly); goreleaser overrides them via -ldflags.
var (
	version = "dev"
	commit  = ""
	date    = ""
)

const starterConfig = `# Aeolus configuration.
# Docs + client setup: https://github.com/aeolus-labs/aeolus

upstreams: []
# Add upstreams via the dashboard at http://localhost:8765 (Settings tab),
# or define them here directly. Example:
#
#   - name: filesystem
#     command: npx
#     args: ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"]

tools:
  allow: []
  deny: []

log:
  level: info
  format: json

dashboard:
  enabled: true
  addr: "localhost:8765"
`

func main() {
	if len(os.Args) >= 2 {
		switch os.Args[1] {
		case "init":
			handleInit(os.Args[2:])
			return
		case "mcp":
			handleMCPBridge(os.Args[2:])
			return
		case "help", "--help", "-h":
			printUsage()
			return
		}
	}

	var (
		showVersion = flag.Bool("version", false, "print version and exit")
		configPath  = flag.String("config", "", "path to config file")
	)
	flag.Parse()

	if *showVersion {
		extra := ""
		if commit != "" {
			extra = " (" + commit
			if date != "" {
				extra += ", " + date
			}
			extra += ")"
		}
		fmt.Printf("aeolus %s%s\n", version, extra)
		return
	}
	if *configPath == "" {
		fmt.Fprintln(os.Stderr, "error: --config is required (or run `aeolus init` to create one)")
		os.Exit(2)
	}

	if err := run(*configPath); err != nil {
		fmt.Fprintln(os.Stderr, "aeolus:", err)
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Print(`Aeolus — local gateway for MCP servers.

Usage:
  aeolus --config <path>    run as a daemon (dashboard + HTTP MCP endpoint)
  aeolus mcp [flags]        stdio bridge to a running daemon (for MCP clients
                            that don't support HTTP transport)
  aeolus init [flags]       write a starter aeolus.yaml
  aeolus --version          print version
  aeolus --help             this help

Daemon flags:
  --config <path>           path to aeolus.yaml (required)

mcp bridge flags:
  --daemon <url>            URL of running daemon's MCP endpoint
                            (default: http://localhost:8765/mcp)

Init flags:
  --path <path>             where to write the config (default: aeolus.yaml)
  --force                   overwrite an existing file
`)
}

func handleInit(args []string) {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	path := fs.String("path", "aeolus.yaml", "where to write the config")
	force := fs.Bool("force", false, "overwrite an existing file")
	_ = fs.Parse(args)

	if _, err := os.Stat(*path); err == nil && !*force {
		fmt.Fprintf(os.Stderr, "error: %s already exists. Use --force to overwrite.\n", *path)
		os.Exit(1)
	}
	if err := os.WriteFile(*path, []byte(starterConfig), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, "aeolus init:", err)
		os.Exit(1)
	}

	abs, _ := filepath.Abs(*path)
	fmt.Printf("Created %s\n\n", *path)
	fmt.Println("Next steps:")
	fmt.Printf("  1. Run: aeolus --config %s\n", *path)
	fmt.Println("  2. Open: http://localhost:8765")
	fmt.Println("  3. In the Settings tab, click + Add upstream or browse the Catalog")
	fmt.Println("     to wire up your first MCP server.")
	fmt.Println()
	fmt.Println("Point your MCP client (Claude Code, Cursor, Copilot, Zed, etc.) at:")
	fmt.Printf("  command: %s\n", findSelfPath())
	fmt.Printf("  args:    [\"--config\", %q]\n", abs)
	fmt.Println()
	fmt.Println("See README.md for client setup snippets.")
}

func findSelfPath() string {
	p, err := os.Executable()
	if err != nil {
		return "/abs/path/to/aeolus"
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return abs
}

// run starts the long-lived daemon: dashboard + HTTP MCP endpoint +
// upstream pool. It blocks until ctx is canceled (Ctrl+C / SIGTERM).
// MCP clients connect either over HTTP at /mcp directly, or via the
// `aeolus mcp` stdio bridge for clients that only support stdio.
func run(configPath string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}

	logger := newLogger(cfg.Log)
	names := make([]string, len(cfg.Upstreams))
	for i, u := range cfg.Upstreams {
		names[i] = u.Name
	}
	logger.Info("aeolus_start", "version", version, "upstreams", names)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Hard fallback: guarantee exit even if a goroutine deadlocks.
	go func() {
		<-ctx.Done()
		fmt.Fprintln(os.Stderr, "aeolus: shutting down...")
		time.Sleep(5 * time.Second)
		fmt.Fprintln(os.Stderr, "aeolus: shutdown timeout, forcing exit")
		os.Exit(1)
	}()

	upstreams, err := startUpstreams(ctx, cfg.Upstreams, logger)
	if err != nil {
		return err
	}

	var dashSrv *dashboard.Server
	var observer proxy.Observer
	var wg sync.WaitGroup
	if cfg.Dashboard.Enabled {
		dashSrv = dashboard.New(cfg.Dashboard.Addr, logger, cfg)
		observer = func(o proxy.ToolCallObservation) {
			dashSrv.Emit(dashboard.Event{
				Time:      o.Time,
				Upstream:  o.Upstream,
				Tool:      o.Tool,
				LatencyMs: o.Latency.Milliseconds(),
				Status:    o.Status,
			})
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := dashSrv.Run(ctx); err != nil {
				logger.Error("dashboard_error", "error", err.Error())
			}
		}()
	}

	// Build and start the engine — the long-lived shared state.
	filter := proxy.NewToolFilter(cfg.Tools)
	engine := proxy.NewEngine(upstreams, filter, logger, observer)
	if err := engine.Start(ctx); err != nil {
		return err
	}

	// Wire the engine into the dashboard so /mcp can route requests.
	if dashSrv != nil {
		dashSrv.SetEngine(engine)
	}

	reloadFn := func(reloadCtx context.Context, newCfg *config.Config) error {
		newUpstreams, err := startUpstreams(reloadCtx, newCfg.Upstreams, logger)
		if err != nil {
			return err
		}
		if err := engine.Reload(reloadCtx, newUpstreams, proxy.NewToolFilter(newCfg.Tools)); err != nil {
			for _, u := range newUpstreams {
				u.Shutdown(2 * time.Second)
			}
			return err
		}
		if dashSrv != nil {
			dashSrv.SetConfig(newCfg)
		}
		return nil
	}

	if dashSrv != nil {
		dashSrv.SetReloader(configPath, reloadFn)
	}

	// File watcher: hand edits to aeolus.yaml are picked up automatically.
	// The dashboard PUT flow first writes the file then triggers reload —
	// the subsequent watcher event re-reloads, which is harmless.
	go func() {
		err := watcher.Watch(ctx, configPath, 250*time.Millisecond, func() {
			newCfg, err := config.Load(configPath)
			if err != nil {
				logger.Warn("watcher_config_load_failed", "error", err.Error())
				return
			}
			if err := reloadFn(ctx, newCfg); err != nil {
				logger.Error("watcher_reload_failed", "error", err.Error())
				return
			}
			logger.Info("watcher_reloaded")
		}, logger)
		if err != nil {
			logger.Warn("watcher_failed", "error", err.Error())
		}
	}()

	logger.Info("daemon_ready",
		"dashboard", "http://"+cfg.Dashboard.Addr,
		"mcp", "http://"+cfg.Dashboard.Addr+"/mcp",
	)

	// Block until signal.
	<-ctx.Done()

	engine.Stop(shutdownTimeout)
	wg.Wait()
	return nil
}

// startUpstreams launches each configured upstream via the transport-agnostic
// factory and wires stderr forwarding (no-op for non-subprocess transports).
// Caller owns the lifetime; pair with Shutdown on each or call Proxy.Reload
// to hand them off.
func startUpstreams(ctx context.Context, list []config.Upstream, logger *slog.Logger) ([]upstream.Server, error) {
	out := make([]upstream.Server, 0, len(list))
	for _, u := range list {
		srv, err := upstream.New(ctx, u, logger)
		if err != nil {
			for _, started := range out {
				started.Shutdown(2 * time.Second)
			}
			return nil, err
		}
		if r := srv.Stderr(); r != nil {
			go forwardStderr(u.Name, r)
		}
		out = append(out, srv)
	}
	return out, nil
}

// forwardStderr reads `r` line by line and prints each line prefixed with
// "[<name>] " to os.Stderr. This keeps multi-line upstream errors readable.
func forwardStderr(name string, r io.Reader) {
	if r == nil {
		return
	}
	prefix := "[" + name + "] "
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		fmt.Fprintln(os.Stderr, prefix+scanner.Text())
	}
}

func newLogger(cfg config.Log) *slog.Logger {
	level := slog.LevelInfo
	switch cfg.Level {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	opts := &slog.HandlerOptions{Level: level}
	var handler slog.Handler
	if cfg.Format == "text" {
		handler = slog.NewTextHandler(os.Stderr, opts)
	} else {
		handler = slog.NewJSONHandler(os.Stderr, opts)
	}
	return slog.New(handler)
}
