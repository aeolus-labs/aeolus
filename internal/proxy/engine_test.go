package proxy_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/aeolus-labs/aeolus/internal/config"
	"github.com/aeolus-labs/aeolus/internal/mcp"
	"github.com/aeolus-labs/aeolus/internal/proxy"
	"github.com/aeolus-labs/aeolus/internal/upstream"
)

// buildFakeUpstream wires an in-memory upstream.Server backed by a
// fakeUpstream (defined in integration_test.go). Returns the
// upstream.Server plus a closer that tears down the pipes.
func buildFakeUpstream(t *testing.T, name string, tools []mcp.Tool, log *slog.Logger) (upstream.Server, func()) {
	t.Helper()
	proxySide, fakeSide := net.Pipe()
	upConn := mcp.NewConn(proxySide, proxySide)
	fakeConn := mcp.NewConn(fakeSide, fakeSide)

	f := &fakeUpstream{name: name, serverInfo: mcp.Info{Name: name}}
	f.setTools(tools)
	go f.run(fakeConn)

	u := upstream.FromConn(name, upConn, log)
	return u, func() {
		_ = proxySide.Close()
		_ = fakeSide.Close()
	}
}

// findToolNames pulls the exposed (prefixed) names out of a tool list
// so tests can assert on a deterministic, sorted slice.
func findToolNames(tools []mcp.Tool) map[string]bool {
	out := make(map[string]bool, len(tools))
	for _, t := range tools {
		out[t.Name] = true
	}
	return out
}

func TestEngine_ReconnectUpstream_SwapsInPlace(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	alphaV1, closeA1 := buildFakeUpstream(t, "alpha", []mcp.Tool{{Name: "v1_tool"}}, log)
	beta, closeB := buildFakeUpstream(t, "beta", []mcp.Tool{{Name: "stable"}}, log)
	t.Cleanup(closeA1)
	t.Cleanup(closeB)

	e := proxy.NewEngine([]upstream.Server{alphaV1, beta}, proxy.NewToolFilter(config.Tools{}), log, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer e.Stop(time.Second)

	if err := e.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Initial tool list should reflect v1 plus beta.
	names := findToolNames(e.ListTools())
	if !names["alpha.v1_tool"] || !names["beta.stable"] {
		t.Fatalf("initial ListTools missing expected tools: %v", names)
	}

	// Build a v2 alpha exposing a different tool and reconnect.
	alphaV2, closeA2 := buildFakeUpstream(t, "alpha", []mcp.Tool{{Name: "v2_tool"}}, log)
	t.Cleanup(closeA2)
	err := e.ReconnectUpstream(ctx, "alpha", func() (upstream.Server, error) {
		return alphaV2, nil
	})
	if err != nil {
		t.Fatalf("ReconnectUpstream: %v", err)
	}

	// After reconnect: v2 visible, v1 gone, beta still there (untouched).
	names = findToolNames(e.ListTools())
	if names["alpha.v1_tool"] {
		t.Errorf("v1 tool should be gone after reconnect")
	}
	if !names["alpha.v2_tool"] {
		t.Errorf("v2 tool should be present after reconnect, got %v", names)
	}
	if !names["beta.stable"] {
		t.Errorf("beta should be untouched by reconnect, got %v", names)
	}
}

func TestEngine_ReconnectUpstream_ClearsPriorFailure(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	// Closed-pipe upstream — Initialize will immediately fail.
	dead, deadFakeSide := net.Pipe()
	_ = deadFakeSide.Close()
	deadConn := mcp.NewConn(dead, dead)
	deadUpstream := upstream.FromConn("acme", deadConn, log)

	e := proxy.NewEngine([]upstream.Server{deadUpstream}, proxy.NewToolFilter(config.Tools{}), log, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer e.Stop(time.Second)

	if err := e.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	failures := e.FailedUpstreams()
	if len(failures) != 1 || failures[0].Name != "acme" {
		t.Fatalf("expected acme in failures, got %+v", failures)
	}

	// Reconnect with a healthy fake; failure should clear.
	healthy, closeH := buildFakeUpstream(t, "acme", []mcp.Tool{{Name: "tool"}}, log)
	t.Cleanup(closeH)
	err := e.ReconnectUpstream(ctx, "acme", func() (upstream.Server, error) {
		return healthy, nil
	})
	if err != nil {
		t.Fatalf("ReconnectUpstream: %v", err)
	}

	if got := e.FailedUpstreams(); len(got) != 0 {
		t.Errorf("expected failures empty after successful reconnect, got %+v", got)
	}
	if names := findToolNames(e.ListTools()); !names["acme.tool"] {
		t.Errorf("expected acme.tool after reconnect, got %v", names)
	}
}

func TestEngine_ReconnectUpstream_RecordsNewFailure(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	healthy, closeH := buildFakeUpstream(t, "acme", []mcp.Tool{{Name: "tool"}}, log)
	t.Cleanup(closeH)

	e := proxy.NewEngine([]upstream.Server{healthy}, proxy.NewToolFilter(config.Tools{}), log, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer e.Stop(time.Second)

	if err := e.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := e.FailedUpstreams(); len(got) != 0 {
		t.Fatalf("expected no failures initially, got %+v", got)
	}

	// Reconnect with a dead-pipe upstream; init should fail and be
	// recorded.
	dead, deadFakeSide := net.Pipe()
	_ = deadFakeSide.Close()
	deadConn := mcp.NewConn(dead, dead)
	deadUpstream := upstream.FromConn("acme", deadConn, log)

	err := e.ReconnectUpstream(ctx, "acme", func() (upstream.Server, error) {
		return deadUpstream, nil
	})
	if err == nil {
		t.Fatalf("expected ReconnectUpstream to return error for failed init")
	}
	failures := e.FailedUpstreams()
	if len(failures) != 1 || failures[0].Name != "acme" {
		t.Errorf("expected acme failure recorded, got %+v", failures)
	}
}
