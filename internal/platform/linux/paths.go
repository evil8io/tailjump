//go:build linux

// Package linux implements the tj platform interfaces for Linux: the TUN
// device, the routes, the session runner, and the standard directories. The
// Resolver stays a stub until the DNS chunk fills it in.
package linux

import (
	"os"
	"path/filepath"
)

// Paths implements platform.Paths for Linux with the XDG base directories.
type Paths struct{}

func NewPaths() *Paths { return &Paths{} }

func (p *Paths) ConfigDir() string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "tj")
	}
	return filepath.Join(homeDir(), ".config", "tj")
}

func (p *Paths) CacheDir() string {
	if dir := os.Getenv("XDG_CACHE_HOME"); dir != "" {
		return filepath.Join(dir, "tj")
	}
	return filepath.Join(homeDir(), ".cache", "tj")
}

func (p *Paths) RuntimeDir() string {
	return "/run/tj"
}

func homeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}
