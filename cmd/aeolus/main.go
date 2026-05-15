package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/aeolus-labs/aeolus/internal/config"
	"github.com/aeolus-labs/aeolus/internal/mcp"
	"github.com/aeolus-labs/aeolus/internal/proxy"
	"github.com/aeolus-labs/aeolus/internal/upstream"
)

const version = "0.0.1-dev"

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
	logger.Info("aeolus_start", "version", version, "upstream", cfg.Upstreams[0].Name)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	up := cfg.Upstreams[0]
	upstreamProc, err := upstream.Start(ctx, up.Name, up.Command, up.Args)
	if err != nil {
		return err
	}
	go func() { _, _ = io.Copy(os.Stderr, upstreamProc.Stderr()) }()

	clientConn := mcp.NewConn(os.Stdin, os.Stdout)
	filter := proxy.NewToolFilter(cfg.Tools)
	p := proxy.New(clientConn, upstreamProc.Conn(), filter, logger)

	runErr := p.Run(ctx)
	waitErr := upstreamProc.Wait()
	if runErr != nil {
		return runErr
	}
	return waitErr
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
