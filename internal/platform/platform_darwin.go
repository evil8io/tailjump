//go:build darwin

package platform

import "github.com/evil8io/tailjump/internal/platform/darwin"

var (
	_ Device   = (*darwin.Device)(nil)
	_ Router   = (*darwin.Router)(nil)
	_ Resolver = (*darwin.Resolver)(nil)
	_ Runner   = (*darwin.Runner)(nil)
	_ Paths    = (*darwin.Paths)(nil)
)

func newDevice() Device     { return darwin.NewDevice() }
func newRouter() Router     { return darwin.NewRouter() }
func newResolver() Resolver { return darwin.NewResolver() }
func newRunner() Runner     { return darwin.NewRunner() }
func newPaths() Paths       { return darwin.NewPaths() }
