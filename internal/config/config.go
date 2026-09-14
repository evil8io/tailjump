// Package config reads the local tj config file.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"

	"github.com/evil8io/tailjump/internal/dns"
	"github.com/evil8io/tailjump/internal/manifest"
	"github.com/evil8io/tailjump/internal/protocols"
	"github.com/evil8io/tailjump/internal/transport"
)

// Config is the parsed $XDG_CONFIG_HOME/tj/config.yaml.
type Config struct {
	Version  int                     `yaml:"version"`
	Defaults Defaults                `yaml:"defaults"`
	Exclude  []string                `yaml:"exclude,omitempty"`
	Remotes  map[string]RemoteConfig `yaml:"remotes"`
}

// Defaults holds the fallback values for a connect.
type Defaults struct {
	User      string `yaml:"user,omitempty"`
	DNS       string `yaml:"dns,omitempty"`
	Transport string `yaml:"transport,omitempty"`
	Protocols string `yaml:"protocols,omitempty"`
}

// RemoteConfig is one entry under remotes.
type RemoteConfig struct {
	Host      string   `yaml:"host,omitempty" json:"host,omitempty"`
	User      string   `yaml:"user,omitempty" json:"user,omitempty"`
	DNS       string   `yaml:"dns,omitempty" json:"dns,omitempty"`
	Transport string   `yaml:"transport,omitempty" json:"transport,omitempty"`
	Protocols string   `yaml:"protocols,omitempty" json:"protocols,omitempty"`
	Networks  []string `yaml:"networks,omitempty" json:"networks,omitempty"`
	Exclude   []string `yaml:"exclude,omitempty" json:"exclude,omitempty"`
}

// Load reads the config at path, decodes it with unknown fields rejected,
// and validates it. A missing file returns a zero Config. See
// docs/architecture.md, "Config file".
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &Config{}, nil
	}
	if err != nil {
		return nil, err
	}
	var c Config
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	if err := validate(&c); err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	return &c, nil
}

// validate checks the version, the DNS, transport, and protocols values,
// every CIDR, and that each remote has a host. An empty DNS, transport, or
// protocols value is not set and is valid; the command that reads it
// applies its own default.
func validate(c *Config) error {
	if c.Version != 0 && c.Version != 1 {
		return fmt.Errorf("version: invalid value %d, want 1", c.Version)
	}
	if err := validateValues("defaults", c.Defaults.DNS, c.Defaults.Transport, c.Defaults.Protocols); err != nil {
		return err
	}
	if _, err := manifest.ParsePrefixes(c.Exclude); err != nil {
		return fmt.Errorf("exclude: %w", err)
	}

	names := make([]string, 0, len(c.Remotes))
	for name := range c.Remotes {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		rc := c.Remotes[name]
		if rc.Host == "" {
			return fmt.Errorf("remotes.%s: host is required", name)
		}
		if err := validateValues("remotes."+name, rc.DNS, rc.Transport, rc.Protocols); err != nil {
			return err
		}
		if _, err := manifest.ParsePrefixes(rc.Networks); err != nil {
			return fmt.Errorf("remotes.%s.networks: %w", name, err)
		}
		if _, err := manifest.ParsePrefixes(rc.Exclude); err != nil {
			return fmt.Errorf("remotes.%s.exclude: %w", name, err)
		}
	}
	return nil
}

// validateValues checks the DNS, transport, and protocols value of one
// defaults or remote block, under prefix.
func validateValues(prefix, dnsValue, transportValue, protocolsValue string) error {
	if dnsValue != "" && !dns.Valid(dnsValue) {
		return fmt.Errorf("%s.dns: invalid value %q, want none, split, or all", prefix, dnsValue)
	}
	if transportValue != "" && !transport.Valid(transportValue) {
		return fmt.Errorf("%s.transport: invalid value %q, want auto, quic, or ssh", prefix, transportValue)
	}
	if protocolsValue != "" && !protocols.Valid(protocolsValue) {
		return fmt.Errorf("%s.protocols: invalid value %q, want a comma-separated list of tcp, udp, and icmp, each at most once", prefix, protocolsValue)
	}
	return nil
}

// Save writes c to path as YAML. It creates the parent directory when it is
// absent. The write replaces the whole file, so it does not keep the
// comments of a file an engineer edited by hand.
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
