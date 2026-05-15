// Package upstream manages a single MCP server running as a subprocess
// communicating over stdio.
package upstream

import (
	"context"
	"fmt"
	"io"
	"os/exec"

	"github.com/aeolus-labs/aeolus/internal/mcp"
)

type Upstream struct {
	Name string

	cmd    *exec.Cmd
	conn   *mcp.Conn
	stderr io.ReadCloser
}

// Start launches the upstream subprocess. Caller must Wait to clean up.
func Start(ctx context.Context, name, command string, args []string) (*Upstream, error) {
	cmd := exec.CommandContext(ctx, command, args...)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("upstream %s: stdin pipe: %w", name, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("upstream %s: stdout pipe: %w", name, err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("upstream %s: stderr pipe: %w", name, err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("upstream %s: start: %w", name, err)
	}
	return &Upstream{
		Name:   name,
		cmd:    cmd,
		conn:   mcp.NewConn(stdout, stdin),
		stderr: stderr,
	}, nil
}

func (u *Upstream) Conn() *mcp.Conn { return u.conn }

// Stderr returns the upstream subprocess's stderr stream.
func (u *Upstream) Stderr() io.Reader { return u.stderr }

func (u *Upstream) Wait() error { return u.cmd.Wait() }
