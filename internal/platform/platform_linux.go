//go:build linux

package platform

import "github.com/evil8io/tailjump/internal/platform/linux"

var (
	_ Device   = (*linux.Device)(nil)
	_ Router   = (*linux.Router)(nil)
	_ Resolver = (*linux.Resolver)(nil)
	_ Runner   = (*linux.Runner)(nil)
	_ Paths    = (*linux.Paths)(nil)
)

func newDevice() Device     { return linux.NewDevice() }
func newRouter() Router     { return linux.NewRouter() }
func newResolver() Resolver { return linux.NewResolver() }
func newRunner() Runner     { return linux.NewRunner() }
func newPaths() Paths       { return linux.NewPaths() }
