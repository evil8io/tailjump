// Package manifest has the manifest schema, the discovery merge, and the
// session network computation. It performs no I/O.
package manifest

import (
	"errors"
	"fmt"
	"net/netip"

	"gopkg.in/yaml.v3"

	"github.com/evil8io/tailjump/internal/transport"
)

// Manifest is the schema version 1 document that a remote advertises.
type Manifest struct {
	Version     int        `yaml:"version"`
	Name        string     `yaml:"name"`
	Description string     `yaml:"description"`
	Discovery   *Discovery `yaml:"discovery"`
	Networks    []string   `yaml:"networks"`
	Exclude     []string   `yaml:"exclude"`
	DNS         *DNS       `yaml:"dns"`
	Checks      []Check    `yaml:"checks"`
	Transport   *Transport `yaml:"transport"`
}

// Transport holds the QUIC transport settings of a remote. An absent section
// means the default port range and the BBR controller.
type Transport struct {
	QUICPorts string     `yaml:"quic_ports"`
	Bandwidth *Bandwidth `yaml:"bandwidth"`
}

// Bandwidth selects the Brutal controller at the given rates. Up is the rate
// from the client to the remote, down the rate from the remote to the client.
type Bandwidth struct {
	Up   string `yaml:"up"`
	Down string `yaml:"down"`
}

// Discovery turns discovery sources on or off. An absent section means all on.
type Discovery struct {
	LinkRoutes    *bool `yaml:"link_routes"`
	CloudMetadata *bool `yaml:"cloud_metadata"`
}

// DNS holds the manifest DNS servers and domains.
type DNS struct {
	Servers []string `yaml:"servers"`
	Domains []string `yaml:"domains"`
}

// Check is a TCP endpoint that tj doctor tests.
type Check struct {
	Name string `yaml:"name"`
	TCP  string `yaml:"tcp"`
}

// Parse reads a manifest and checks the schema version.
func Parse(b []byte) (*Manifest, error) {
	var m Manifest
	if err := yaml.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	if m.Version != 1 {
		return nil, fmt.Errorf("unsupported manifest version %d, want 1", m.Version)
	}
	if _, err := m.QUICPorts(); err != nil {
		return nil, err
	}
	if _, _, err := m.Bandwidth(); err != nil {
		return nil, err
	}
	return &m, nil
}

// QUICPorts returns the port range the helper listens in, the default when
// the manifest sets none.
func (m *Manifest) QUICPorts() (transport.PortRange, error) {
	if m.Transport == nil || m.Transport.QUICPorts == "" {
		return transport.DefaultPorts, nil
	}
	r, err := transport.ParsePortRange(m.Transport.QUICPorts)
	if err != nil {
		return transport.PortRange{}, fmt.Errorf("transport.quic_ports: %w", err)
	}
	return r, nil
}

// Bandwidth returns the Brutal rates in bytes per second, up and down, and
// zero for both when the manifest sets no bandwidth.
func (m *Manifest) Bandwidth() (up, down uint64, err error) {
	if m.Transport == nil || m.Transport.Bandwidth == nil {
		return 0, 0, nil
	}
	b := m.Transport.Bandwidth
	if b.Up == "" || b.Down == "" {
		return 0, 0, errors.New("transport.bandwidth needs both up and down")
	}
	if up, err = transport.ParseRate(b.Up); err != nil {
		return 0, 0, fmt.Errorf("transport.bandwidth.up: %w", err)
	}
	if down, err = transport.ParseRate(b.Down); err != nil {
		return 0, 0, fmt.Errorf("transport.bandwidth.down: %w", err)
	}
	return up, down, nil
}

// Empty returns a manifest that stands for a remote with no manifest file.
func Empty() *Manifest { return &Manifest{Version: 1} }

// LinkRoutesEnabled reports whether the link-routes source is on.
func (m *Manifest) LinkRoutesEnabled() bool {
	if m.Discovery == nil || m.Discovery.LinkRoutes == nil {
		return true
	}
	return *m.Discovery.LinkRoutes
}

// CloudEnabled reports whether the cloud-metadata source is on.
func (m *Manifest) CloudEnabled() bool {
	if m.Discovery == nil || m.Discovery.CloudMetadata == nil {
		return true
	}
	return *m.Discovery.CloudMetadata
}

// ParsePrefixes parses CIDR strings into masked prefixes.
func ParsePrefixes(cidrs []string) ([]netip.Prefix, error) {
	out := make([]netip.Prefix, 0, len(cidrs))
	for _, s := range cidrs {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return nil, fmt.Errorf("parse prefix %q: %w", s, err)
		}
		out = append(out, p.Masked())
	}
	return out, nil
}

// ParseAddrs parses IP address strings.
func ParseAddrs(addrs []string) ([]netip.Addr, error) {
	out := make([]netip.Addr, 0, len(addrs))
	for _, s := range addrs {
		a, err := netip.ParseAddr(s)
		if err != nil {
			return nil, fmt.Errorf("parse addr %q: %w", s, err)
		}
		out = append(out, a)
	}
	return out, nil
}
