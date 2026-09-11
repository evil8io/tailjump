//go:build darwin

// Package darwin implements the tj platform interfaces for macOS: a utun
// device configured with the ifconfig and route tools, DNS through
// /etc/resolver and networksetup, and a detached child process with a
// state file in place of a systemd unit.
package darwin

import (
	"os"
	"path/filepath"
)

// runtimeDir is a var, not a const, so a test can point it at a temporary
// directory instead of the real /var/run/tj, which needs root to write.
var runtimeDir = "/var/run/tj"

// Paths implements platform.Paths for macOS.
type Paths struct{}

func NewPaths() *Paths { return &Paths{} }

func (p *Paths) ConfigDir() string {
	return filepath.Join(homeDir(), "Library", "Application Support", "tj")
}

func (p *Paths) CacheDir() string {
	return filepath.Join(homeDir(), "Library", "Caches", "tj")
}

func (p *Paths) RuntimeDir() string {
	return runtimeDir
}

func homeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}
