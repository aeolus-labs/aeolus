// Package config loads and validates the Aeolus YAML configuration.
package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Upstreams []Upstream `yaml:"upstreams"`
	Tools     Tools      `yaml:"tools"`
	Log       Log        `yaml:"log"`
}

type Upstream struct {
	Name    string   `yaml:"name"`
	Command string   `yaml:"command"`
	Args    []string `yaml:"args"`
	Env     []string `yaml:"env,omitempty"`
}

type Tools struct {
	Allow []string `yaml:"allow"`
	Deny  []string `yaml:"deny"`
}

type Log struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
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
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("invalid config %s: %w", path, err)
	}
	return &cfg, nil
}

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
	return nil
}
