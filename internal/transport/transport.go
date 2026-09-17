// Package transport has the transport mode, the QUIC port range, the
// bandwidth rate parser, and the congestion controller choice. The client,
// the helper, the manifest, and the config share it. It imports the standard
// library only, so the helper that links it stays small.
package transport

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Mode selects the data plane transport of a session.
type Mode string

// The transport modes. auto tries QUIC and falls back to the SSH transport.
const (
	ModeAuto Mode = "auto"
	ModeQUIC Mode = "quic"
	ModeSSH  Mode = "ssh"
)

// Valid reports whether s names a transport mode.
func Valid(s string) bool {
	switch Mode(s) {
	case ModeAuto, ModeQUIC, ModeSSH:
		return true
	}
	return false
}

// Resolve picks the mode by precedence: the flag, then the remote config,
// then the defaults, then auto.
func Resolve(flag, remote, defaults string) Mode {
	for _, s := range []string{flag, remote, defaults} {
		if s != "" {
			return Mode(s)
		}
	}
	return ModeAuto
}

// PortRange is the inclusive UDP port range the helper listens in.
type PortRange struct {
	First uint16
	Last  uint16
}

// DefaultPorts is the port range when the manifest sets none.
var DefaultPorts = PortRange{First: 7443, Last: 7452}

// ParsePortRange parses "first-last" or a single port.
func ParsePortRange(s string) (PortRange, error) {
	s = strings.TrimSpace(s)
	first, last, found := strings.Cut(s, "-")
	if !found {
		last = first
	}
	lo, err := parsePort(first)
	if err != nil {
		return PortRange{}, fmt.Errorf("port range %q: %w", s, err)
	}
	hi, err := parsePort(last)
	if err != nil {
		return PortRange{}, fmt.Errorf("port range %q: %w", s, err)
	}
	if lo > hi {
		return PortRange{}, fmt.Errorf("port range %q: the first port is above the last", s)
	}
	return PortRange{First: lo, Last: hi}, nil
}

func parsePort(s string) (uint16, error) {
	n, err := strconv.ParseUint(strings.TrimSpace(s), 10, 16)
	if err != nil || n == 0 {
		return 0, fmt.Errorf("invalid port %q", s)
	}
	return uint16(n), nil
}

// String formats the range as "first-last".
func (r PortRange) String() string {
	return fmt.Sprintf("%d-%d", r.First, r.Last)
}

// Contains reports whether p is in the range.
func (r PortRange) Contains(p uint16) bool {
	return p >= r.First && p <= r.Last
}

// PolicyRule is the tailnet policy entry that passes the range, in the
// grant syntax, for the fallback warning and the docs.
func (r PortRange) PolicyRule() string {
	return fmt.Sprintf("udp:%d-%d", r.First, r.Last)
}

var rateUnits = map[string]uint64{
	"bps":  1,
	"kbps": 1_000,
	"mbps": 1_000_000,
	"gbps": 1_000_000_000,
	"tbps": 1_000_000_000_000,
}

// ParseRate parses a bandwidth such as "20 mbps" and returns bytes per
// second. The unit is bps, kbps, mbps, gbps, or tbps, in bits per second,
// case-insensitive, with an optional space before it.
func ParseRate(s string) (uint64, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	i := strings.IndexFunc(s, func(r rune) bool { return r < '0' || r > '9' })
	if i <= 0 {
		return 0, fmt.Errorf("rate %q: want a number and a unit, for example 20 mbps", s)
	}
	unit := strings.TrimSpace(s[i:])
	mult, ok := rateUnits[unit]
	if !ok {
		return 0, fmt.Errorf("rate %q: unknown unit %q, want bps, kbps, mbps, gbps, or tbps", s, unit)
	}
	n, err := strconv.ParseUint(s[:i], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("rate %q: %w", s, err)
	}
	if n == 0 {
		return 0, fmt.Errorf("rate %q: zero is not a rate", s)
	}
	if n > math.MaxUint64/mult/8*8 {
		return 0, fmt.Errorf("rate %q: too large", s)
	}
	return n * mult / 8, nil
}

// FormatRate prints a rate in bytes per second as a whole number with the
// largest unit that keeps it at 10 or more, for example "20 mbps" and
// "1200 mbps". ParseRate accepts every output.
func FormatRate(bytesPerSecond uint64) string {
	bits := bytesPerSecond * 8
	unit := "bps"
	for _, u := range []string{"kbps", "mbps", "gbps", "tbps"} {
		if bits < 10*rateUnits[u] {
			break
		}
		unit = u
	}
	return fmt.Sprintf("%d %s", (bits+rateUnits[unit]/2)/rateUnits[unit], unit)
}

// Controller names the congestion controller of one sender. Bps is the
// Brutal rate in bytes per second and zero for BBR.
type Controller struct {
	Name string
	Bps  uint64
}

// The controller names. Cubic is the library default and exists as a
// measurement knob only; see docs/architecture.md, "Testing".
const (
	BBR    = "bbr"
	Brutal = "brutal"
	Cubic  = "cubic"
)

// ControllerFor returns Brutal at the rate when it is set, otherwise BBR.
func ControllerFor(bytesPerSecond uint64) Controller {
	if bytesPerSecond > 0 {
		return Controller{Name: Brutal, Bps: bytesPerSecond}
	}
	return Controller{Name: BBR}
}

// ControllerNamed returns the named controller when the name is cubic, and
// otherwise the rule of ControllerFor.
func ControllerNamed(name string, bytesPerSecond uint64) Controller {
	if name == Cubic {
		return Controller{Name: Cubic}
	}
	return ControllerFor(bytesPerSecond)
}

// String formats the controller for the control line: "bbr", "cubic", or
// "brutal=<bytes-per-second>".
func (c Controller) String() string {
	switch c.Name {
	case Brutal:
		return fmt.Sprintf("%s=%d", Brutal, c.Bps)
	case Cubic:
		return Cubic
	default:
		return BBR
	}
}

// ParseController reverses String.
func ParseController(s string) (Controller, error) {
	name, rate, found := strings.Cut(s, "=")
	switch {
	case name == BBR && !found:
		return Controller{Name: BBR}, nil
	case name == Cubic && !found:
		return Controller{Name: Cubic}, nil
	case name == Brutal && found:
		bps, err := strconv.ParseUint(rate, 10, 64)
		if err != nil || bps == 0 {
			return Controller{}, fmt.Errorf("controller %q: brutal needs a rate in bytes per second", s)
		}
		return Controller{Name: Brutal, Bps: bps}, nil
	}
	return Controller{}, errors.New("controller " + strconv.Quote(s) + ": want bbr, cubic, or brutal=<bytes-per-second>")
}
