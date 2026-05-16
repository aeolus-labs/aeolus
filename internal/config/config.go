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
}

type Dashboard struct {
	Enabled bool   `yaml:"enabled" json:"enabled"`
	Addr    string `yaml:"addr" json:"addr"`
}

type Upstream struct {
	Name    string   `yaml:"name" json:"name"`
	Command string   `yaml:"command" json:"command"`
	Args    []string `yaml:"args" json:"args"`
	Env     []string `yaml:"env,omitempty" json:"env,omitempty"`
}

type Tools struct {
	Allow []string `yaml:"allow" json:"allow"`
	Deny  []string `yaml:"deny" json:"deny"`
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
	if len(c.Upstreams) == 0 {
		return fmt.Errorf("at least one upstream is required")
	}
	seen := make(map[string]bool, len(c.Upstreams))
	for i, u := range c.Upstreams {
		if u.Name == "" {
			return fmt.Errorf("upstreams[%d]: name is required", i)
		}
		if seen[u.Name] {
			return fmt.Errorf("upstreams[%d]: duplicate name %q", i, u.Name)
		}
		seen[u.Name] = true
		if u.Command == "" {
			return fmt.Errorf("upstreams[%d] (%s): command is required", i, u.Name)
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
		return fmt.Errorf("log.format must be json or text; got %q", c.Log.Format)
	}
	if c.Dashboard.Enabled && c.Dashboard.Addr == "" {
		c.Dashboard.Addr = "localhost:8765"
	}
	return nil
}
