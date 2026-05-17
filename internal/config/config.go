// Package config loads and validates the Aeolus YAML configuration.
package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Upstreams []Upstream `yaml:"upstreams" json:"upstreams"`
	Tools     Tools      `yaml:"tools" json:"tools"`
	Log       Log        `yaml:"log" json:"log"`
	Dashboard Dashboard  `yaml:"dashboard" json:"dashboard"`

	// Workspaces let one daemon expose different upstream subsets per
	// directory/client. When empty, every connection sees every
	// upstream (the pre-workspace behavior).
	Workspaces []Workspace `yaml:"workspaces,omitempty" json:"workspaces,omitempty"`
}

// WorkspaceByName returns the workspace with the given name, or nil if
// none matches. The lookup is case-sensitive.
func (c *Config) WorkspaceByName(name string) *Workspace {
	for i := range c.Workspaces {
		if c.Workspaces[i].Name == name {
			return &c.Workspaces[i]
		}
	}
	return nil
}

type Dashboard struct {
	Enabled bool   `yaml:"enabled" json:"enabled"`
	Addr    string `yaml:"addr" json:"addr"`
}

type Upstream struct {
	Name      string `yaml:"name" json:"name"`
	Transport string `yaml:"transport,omitempty" json:"transport,omitempty"` // "stdio" (default) or "http"

	// Enabled is a pointer so an absent field reads as "default true" —
	// existing aeolus.yaml files without the field keep working. Set to
	// false to turn off this upstream without removing its config.
	Enabled *bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`

	// stdio transport fields
	Command string   `yaml:"command,omitempty" json:"command,omitempty"`
	Args    []string `yaml:"args,omitempty" json:"args,omitempty"`
	Env     []string `yaml:"env,omitempty" json:"env,omitempty"`

	// http transport fields
	URL     string            `yaml:"url,omitempty" json:"url,omitempty"`
	Headers map[string]string `yaml:"headers,omitempty" json:"headers,omitempty"`
}

// IsEnabled reports whether this upstream should be initialized. Treats
// a missing Enabled field as "true" so existing configs are unchanged.
func (u Upstream) IsEnabled() bool {
	if u.Enabled == nil {
		return true
	}
	return *u.Enabled
}

type Tools struct {
	Allow []string `yaml:"allow" json:"allow"`
	Deny  []string `yaml:"deny" json:"deny"`
}

// Workspace is a named "view" over the configured upstreams. Each
// MCP client connection resolves to a workspace (via --workspace flag,
// cwd match, or "default" fallback) and only sees tools from that
// workspace's `Include` list.
//
// Workspaces let one Aeolus daemon expose different tool sets per
// project: a Claude Code session in ~/code/project-a sees one set,
// the same daemon hit with `--workspace project-b` from another
// directory sees a different set.
type Workspace struct {
	Name string `yaml:"name,omitempty" json:"name"`

	// Include lists the names of top-level upstreams visible in this
	// workspace. Empty means "no upstreams" — explicit, not a wildcard.
	Include []string `yaml:"include,omitempty" json:"include,omitempty"`

	// CWDMatch is a list of directory paths or globs. When `aeolus mcp`
	// is spawned without `--workspace`, its working directory is matched
	// against these patterns to auto-select the workspace.
	//
	// Patterns:
	//   - exact path:   /Users/me/code/foo
	//   - ~ expansion:  ~/code/foo
	//   - trailing /**: ~/code/foo/**   (match path itself or any descendant)
	CWDMatch []string `yaml:"cwd_match,omitempty" json:"cwd_match,omitempty"`

	// Tools layered on top of the global Tools.allow/deny. Workspace
	// rules are evaluated *after* the global ones (intersection —
	// either side can deny).
	Tools Tools `yaml:"tools,omitempty" json:"tools,omitempty"`
}

type Log struct {
	Level  string `yaml:"level" json:"level"`
	Format string `yaml:"format" json:"format"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	if err := Validate(&cfg); err != nil {
		return nil, fmt.Errorf("invalid config %s: %w", path, err)
	}
	return &cfg, nil
}

// Validate checks the config and fills in defaults. Exported so the dashboard
// can validate a posted config before persisting it.
func Validate(c *Config) error { return c.validate() }

func (c *Config) validate() error {
	// Empty upstreams is fine — useful for first-run, where the user runs
	// `aeolus init` and then adds upstreams via the dashboard.
	seen := make(map[string]bool, len(c.Upstreams))
	for i, u := range c.Upstreams {
		if u.Name == "" {
			return fmt.Errorf("upstream at position %d is missing a name", i)
		}
		if seen[u.Name] {
			return fmt.Errorf("upstream at position %d reuses the name %q — names must be unique", i, u.Name)
		}
		seen[u.Name] = true
		transport := u.Transport
		if transport == "" {
			transport = "stdio"
			c.Upstreams[i].Transport = "stdio"
		}
		switch transport {
		case "stdio":
			if u.Command == "" {
				return fmt.Errorf("upstream %q needs a command for stdio transport (e.g. \"npx\")", u.Name)
			}
		case "http":
			if u.URL == "" {
				return fmt.Errorf("upstream %q needs a url for http transport (e.g. https://example.com/mcp)", u.Name)
			}
		default:
			return fmt.Errorf("upstream %q has unknown transport %q — expected \"stdio\" or \"http\"", u.Name, transport)
		}
	}
	if c.Log.Level == "" {
		c.Log.Level = "info"
	}
	if c.Log.Format == "" {
		c.Log.Format = "json"
	}
	switch c.Log.Format {
	case "json", "text":
	default:
		return fmt.Errorf("log.format must be \"json\" or \"text\"; got %q", c.Log.Format)
	}
	if c.Dashboard.Enabled && c.Dashboard.Addr == "" {
		c.Dashboard.Addr = "localhost:8765"
	}
	// Validate workspaces: unique names, every Include references an
	// existing upstream.
	wsSeen := make(map[string]bool, len(c.Workspaces))
	for i, w := range c.Workspaces {
		if w.Name == "" {
			return fmt.Errorf("workspace at position %d is missing a name", i)
		}
		if wsSeen[w.Name] {
			return fmt.Errorf("workspace at position %d reuses the name %q — workspace names must be unique", i, w.Name)
		}
		wsSeen[w.Name] = true
		for _, inc := range w.Include {
			if !seen[inc] {
				return fmt.Errorf("workspace %q includes upstream %q which is not defined under upstreams:", w.Name, inc)
			}
		}
	}
	return nil
}
