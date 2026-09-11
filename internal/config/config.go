// Package config reads the local tj config file.
package config

import (
	"errors"
	"os"

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
	User string `yaml:"user"`
	DNS  string `yaml:"dns"`
}

// RemoteConfig is one entry under remotes.
type RemoteConfig struct {
	Host string `yaml:"host"`
	User string `yaml:"user"`
	DNS  string `yaml:"dns"`
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
