// Package platform has the interfaces that isolate the OS-specific parts of
// a session: the TUN device, the routes, the DNS resolver, the session
// runner, and the standard directories. internal/platform/linux and
// internal/platform/darwin implement them for real, and
// internal/platform/fake implements them in memory for tests.
package platform

import (
	"context"
	"io"
	"net/netip"
	"time"

	"golang.zx2c4.com/wireguard/tun"
)

type Device interface {
	// Create creates the TUN device with the name and the MTU.
	// It returns the wireguard tun.Device and the real name.
	Create(name string, mtu int) (tun.Device, string, error)
	// Configure sets the addresses and brings the device up.
	Configure(name string, addrs []netip.Prefix) error
	Delete(name string) error
}

type Router interface {
	Add(device string, prefixes []netip.Prefix) error
	Remove(device string, prefixes []netip.Prefix) error
	// Reset removes every session route and rule that Add installed,
	// whether or not the device still exists. It is safe to call twice.
	Reset() error
	// Connected returns the connected subnets of the client,
	// without the loopback and without the tj device.
	Connected() ([]netip.Prefix, error)
}

type Resolver interface {
	// Available reports whether the platform resolver service runs.
	Available() bool
	ApplySplit(device string, servers []netip.Addr, domains []string) error
	ApplyAll(device string, servers []netip.Addr, domains []string) error
	// Revert removes every DNS change for the device. It is safe to call twice.
	Revert(device string) error
}

// LogOptions selects the lines Runner.Logs writes.
type LogOptions struct {
	Lines  int       // the last N lines; 0 with a non-zero Since means every line since Since
	Follow bool      // keep writing new lines until ctx ends
	Since  time.Time // zero means no time limit
}

type Runner interface {
	// Start starts the session process detached with the plan file as its argument.
	Start(plan string) error
	Stop() error
	Active() (bool, error)
	// Logs writes the session log to w, per opts. It stops when ctx ends and
	// returns nil, not the process error, because the caller asked for the
	// stop.
	Logs(ctx context.Context, w io.Writer, opts LogOptions) error
}

type Paths interface {
	ConfigDir() string  // Linux: $XDG_CONFIG_HOME/tj; macOS: ~/Library/Application Support/tj
	CacheDir() string   // Linux: $XDG_CACHE_HOME/tj; macOS: ~/Library/Caches/tj
	RuntimeDir() string // Linux: /run/tj; macOS: /var/run/tj
}

// Platform bundles the five platform interfaces.
type Platform struct {
	Device   Device
	Router   Router
	Resolver Resolver
	Runner   Runner
	Paths    Paths
}

// New returns the Platform for the running OS, selected at build time.
func New() Platform {
	return Platform{
		Device:   newDevice(),
		Router:   newRouter(),
		Resolver: newResolver(),
		Runner:   newRunner(),
		Paths:    newPaths(),
	}
}
