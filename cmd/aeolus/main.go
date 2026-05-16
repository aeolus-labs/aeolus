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
	"sync"
	"syscall"
	"time"

	"github.com/aeolus-labs/aeolus/internal/config"
	"github.com/aeolus-labs/aeolus/internal/dashboard"
	"github.com/aeolus-labs/aeolus/internal/mcp"
	"github.com/aeolus-labs/aeolus/internal/proxy"
	"github.com/aeolus-labs/aeolus/internal/upstream"
)

const shutdownTimeout = 3 * time.Second

const version = "0.2.0-dev"

func main() {
	var (
		showVersion = flag.Bool("version", false, "print version and exit")
		configPath  = flag.String("config", "", "path to config file")
	)
	flag.Parse()

	if *showVersion {
		fmt.Println("aeolus", version)
		return
	}
	if *configPath == "" {
		fmt.Fprintln(os.Stderr, "error: --config is required")
		os.Exit(2)
	}

	if err := run(*configPath); err != nil {
		fmt.Fprintln(os.Stderr, "aeolus:", err)
		os.Exit(1)
	}
}

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

	// Closing stdin on signal unblocks the proxy's blocking Read so Run can
	// return promptly. Without this, Ctrl+C cancels the context but the read
	// keeps blocking and the process never exits.
	go func() {
		<-ctx.Done()
		_ = os.Stdin.Close()
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

	clientConn := mcp.NewConn(os.Stdin, os.Stdout)
	filter := proxy.NewToolFilter(cfg.Tools)
	p := proxy.New(clientConn, upstreams, filter, logger, observer)

	if dashSrv != nil {
		dashSrv.SetReloader(configPath, func(reloadCtx context.Context, newCfg *config.Config) error {
			newUpstreams, err := startUpstreams(reloadCtx, newCfg.Upstreams, logger)
			if err != nil {
				return err
			}
			if err := p.Reload(reloadCtx, newUpstreams, proxy.NewToolFilter(newCfg.Tools)); err != nil {
				for _, u := range newUpstreams {
					u.Shutdown(2 * time.Second)
				}
				return err
			}
			return nil
		})
	}

	runErr := p.Run(ctx)
	cancel()
	for _, u := range upstreams {
		u.Shutdown(shutdownTimeout)
	}
	wg.Wait()
	return runErr
}

// startUpstreams launches each configured upstream subprocess and wires
// stderr forwarding. Caller owns the lifetime; pair with Shutdown on each
// or call Proxy.Reload to hand them off.
func startUpstreams(ctx context.Context, list []config.Upstream, logger *slog.Logger) ([]*upstream.Upstream, error) {
	out := make([]*upstream.Upstream, 0, len(list))
	for _, u := range list {
		proc, err := upstream.Start(ctx, u.Name, u.Command, u.Args, u.Env, logger)
		if err != nil {
			for _, started := range out {
				started.Shutdown(2 * time.Second)
			}
			return nil, err
		}
		go forwardStderr(u.Name, proc.Stderr())
		out = append(out, proc)
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
