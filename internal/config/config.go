// Package config reads the local tj config file.
package config

import (
	"errors"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config is the parsed $XDG_CONFIG_HOME/tj/config.yaml.
type Config struct {
	Version  int                     `yaml:"version"`
	Defaults Defaults                `yaml:"defaults"`
	Exclude  []string                `yaml:"exclude"`
	Remotes  map[string]RemoteConfig `yaml:"remotes"`
}

// Defaults holds the fallback values for a connect.
type Defaults struct {
	User      string `yaml:"user"`
	DNS       string `yaml:"dns"`
	Transport string `yaml:"transport,omitempty"`
}

// RemoteConfig is one entry under remotes.
type RemoteConfig struct {
	Host      string   `yaml:"host,omitempty" json:"host,omitempty"`
	User      string   `yaml:"user,omitempty" json:"user,omitempty"`
	DNS       string   `yaml:"dns,omitempty" json:"dns,omitempty"`
	Transport string   `yaml:"transport,omitempty" json:"transport,omitempty"`
	Networks  []string `yaml:"networks,omitempty" json:"networks,omitempty"`
	Exclude   []string `yaml:"exclude,omitempty" json:"exclude,omitempty"`
}

// Load reads the config at path. A missing file returns a zero Config.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &Config{}, nil
	}
	if err != nil {
		return nil, err
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// Save writes c to path as YAML. It creates the parent directory when it is
// absent.
func Save(path string, c *Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}
