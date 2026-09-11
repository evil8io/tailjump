package session

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"

	"github.com/evil8io/tailjump/internal/dns"
)

const (
	// deviceName is the tj TUN device.
	deviceName = "tj0"
	// deviceMTU is the tj TUN device MTU.
	deviceMTU = 1500

	planFile  = "plan.json"
	stateFile = "session.json"
)

// deviceAddrs are the tj0 addresses. The global-scope ULA is required as an
// IPv6 source: RFC 6724 rejects a link-local source for a global
// destination, so a client without global IPv6 would otherwise have no
// source at all.
var deviceAddrs = []netip.Prefix{
	netip.MustParsePrefix("169.254.117.1/32"),
	netip.MustParsePrefix("fd00:117::1/128"),
	netip.MustParsePrefix("fe80::1/64"),
}

// PlanPath is the plan file under the runtime directory.
func PlanPath(runtimeDir string) string { return filepath.Join(runtimeDir, planFile) }

// StatePath is the state file under the runtime directory.
func StatePath(runtimeDir string) string { return filepath.Join(runtimeDir, stateFile) }

// Marshal encodes a plan as JSON.
func (p *Plan) Marshal() ([]byte, error) { return json.Marshal(p) }

// writePlan writes the plan JSON to path with 0600, because it names the
// remote and the networks.
func writePlan(path string, plan []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create runtime dir: %w", err)
	}
	if err := os.WriteFile(path, plan, 0o600); err != nil {
		return fmt.Errorf("write plan: %w", err)
	}
	return nil
}

// ReadPlan reads and decodes the plan file.
func ReadPlan(path string) (*Plan, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read plan: %w", err)
	}
	var p Plan
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("parse plan: %w", err)
	}
	return &p, nil
}

// writeState writes the state file with 0644, so tj status reads it without
// root.
func writeState(path string, st *State) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create runtime dir: %w", err)
	}
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return fmt.Errorf("write state: %w", err)
	}
	return nil
}

// ReadState reads and decodes the state file. A missing file returns
// os.ErrNotExist wrapped, which the caller treats as no session.
func ReadState(path string) (*State, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var st State
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, fmt.Errorf("parse state: %w", err)
	}
	return &st, nil
}

// planNetworks parses the plan's session networks.
func planNetworks(p *Plan) ([]netip.Prefix, error) {
	out := make([]netip.Prefix, 0, len(p.Networks))
	for _, s := range p.Networks {
		pfx, err := netip.ParsePrefix(s)
		if err != nil {
			return nil, fmt.Errorf("plan network %q: %w", s, err)
		}
		out = append(out, pfx)
	}
	return out, nil
}

// routePrefixes returns every prefix the session routes through tj0: the
// session networks, plus a host route for each DNS server outside them, so
// captured queries reach the resolver. It adds the DNS host routes only when
// the DNS mode changes resolution.
func routePrefixes(p *Plan) ([]netip.Prefix, error) {
	networks, err := planNetworks(p)
	if err != nil {
		return nil, err
	}
	if dns.Mode(p.DNS.Mode) == dns.ModeNone {
		return networks, nil
	}
	for _, s := range p.DNS.Servers {
		addr, err := netip.ParseAddr(s)
		if err != nil {
			return nil, fmt.Errorf("plan dns server %q: %w", s, err)
		}
		if coveredBy(addr, networks) {
			continue
		}
		networks = append(networks, netip.PrefixFrom(addr, addr.BitLen()))
	}
	return networks, nil
}

func coveredBy(addr netip.Addr, prefixes []netip.Prefix) bool {
	for _, p := range prefixes {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}
