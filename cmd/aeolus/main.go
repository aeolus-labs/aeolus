package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/aeolus-labs/aeolus/internal/config"
	"github.com/aeolus-labs/aeolus/internal/dashboard"
	"github.com/aeolus-labs/aeolus/internal/mcp"
	"github.com/aeolus-labs/aeolus/internal/proxy"
	"github.com/aeolus-labs/aeolus/internal/upstream"
)

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

	upstreams := make([]*upstream.Upstream, 0, len(cfg.Upstreams))
	for _, u := range cfg.Upstreams {
		proc, err := upstream.Start(ctx, u.Name, u.Command, u.Args, logger)
		if err != nil {
			return err
		}
		go func(name string, r io.Reader) {
			_, _ = io.Copy(stderrPrefixer{prefix: "[" + name + "] "}, r)
		}(u.Name, proc.Stderr())
		upstreams = append(upstreams, proc)
	}

	var dashSrv *dashboard.Server
	var observer proxy.Observer
	var wg sync.WaitGroup
	if cfg.Dashboard.Enabled {
		dashSrv = dashboard.New(cfg.Dashboard.Addr, logger)
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

	runErr := p.Run(ctx)
	cancel()
	for _, u := range upstreams {
		_ = u.Wait()
	}
	wg.Wait()
	return runErr
}

type stderrPrefixer struct{ prefix string }

func (s stderrPrefixer) Write(p []byte) (int, error) {
	if _, err := os.Stderr.WriteString(s.prefix); err != nil {
		return 0, err
	}
	return os.Stderr.Write(p)
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
