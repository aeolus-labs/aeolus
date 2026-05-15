package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoad_Valid(t *testing.T) {
	path := writeTemp(t, `
upstreams:
  - name: filesystem
    command: npx
    args: ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"]
  - name: github
    command: npx
    args: ["-y", "@modelcontextprotocol/server-github"]
tools:
  allow: ["filesystem.read_*"]
  deny: ["github.delete_*"]
log:
  level: debug
  format: text
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, want := len(cfg.Upstreams), 2; got != want {
		t.Fatalf("Upstreams: got %d, want %d", got, want)
	}
	if cfg.Upstreams[0].Name != "filesystem" || cfg.Upstreams[1].Name != "github" {
		t.Errorf("unexpected upstream names: %+v", cfg.Upstreams)
	}
	if cfg.Log.Level != "debug" || cfg.Log.Format != "text" {
		t.Errorf("log: got %+v", cfg.Log)
	}
}

func TestLoad_Defaults(t *testing.T) {
	path := writeTemp(t, `
upstreams:
  - name: fs
    command: cat
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Log.Level != "info" {
		t.Errorf("default level: got %q, want info", cfg.Log.Level)
	}
	if cfg.Log.Format != "json" {
		t.Errorf("default format: got %q, want json", cfg.Log.Format)
	}
}

func TestLoad_Errors(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantSub string
	}{
		{
			"no upstreams",
			"upstreams: []\n",
			"at least one upstream",
		},
		{
			"missing name",
			"upstreams:\n  - command: cat\n",
			"name is required",
		},
		{
			"missing command",
			"upstreams:\n  - name: x\n",
			"command is required",
		},
		{
			"duplicate name",
			"upstreams:\n  - name: x\n    command: cat\n  - name: x\n    command: cat\n",
			"duplicate name",
		},
		{
			"bad log format",
			"upstreams:\n  - name: x\n    command: cat\nlog:\n  format: xml\n",
			"log.format",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeTemp(t, tt.body)
			_, err := Load(path)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantSub)
			}
			if !strings.Contains(err.Error(), tt.wantSub) {
				t.Errorf("error %q does not contain %q", err.Error(), tt.wantSub)
			}
		})
	}
}

func writeTemp(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "aeolus.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writeTemp: %v", err)
	}
	return path
}
