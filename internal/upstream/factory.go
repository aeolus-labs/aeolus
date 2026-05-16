package upstream

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/aeolus-labs/aeolus/internal/config"
)

// New creates a Server from a config entry, dispatching on Transport.
// Empty Transport defaults to "stdio" for backward compatibility.
func New(ctx context.Context, cfg config.Upstream, log *slog.Logger) (Server, error) {
	transport := cfg.Transport
	if transport == "" {
		transport = "stdio"
	}
	switch transport {
	case "stdio":
		return Start(ctx, cfg.Name, cfg.Command, cfg.Args, cfg.Env, log)
	case "http":
		return StartHTTP(ctx, cfg.Name, cfg.URL, cfg.Headers, log)
	default:
		return nil, fmt.Errorf("upstream %s: unknown transport %q", cfg.Name, transport)
	}
}
