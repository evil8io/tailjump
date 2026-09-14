//go:build darwin

package platform

import (
	"context"
	"io"

	"github.com/evil8io/tailjump/internal/platform/darwin"
)

var (
	_ Device   = (*darwin.Device)(nil)
	_ Router   = (*darwin.Router)(nil)
	_ Resolver = (*darwin.Resolver)(nil)
	_ Runner   = darwinRunner{}
	_ Paths    = (*darwin.Paths)(nil)
)

func newDevice() Device     { return darwin.NewDevice() }
func newRouter() Router     { return darwin.NewRouter() }
func newResolver() Resolver { return darwin.NewResolver() }
func newRunner() Runner     { return darwinRunner{darwin.NewRunner()} }
func newPaths() Paths       { return darwin.NewPaths() }

// darwinRunner adapts darwin.Runner to Runner; see linuxRunner in
// platform_linux.go for the reason.
type darwinRunner struct{ *darwin.Runner }

func (r darwinRunner) Logs(ctx context.Context, w io.Writer, opts LogOptions) error {
	return r.Runner.Logs(ctx, w, opts.Lines, opts.Since, opts.Follow)
}
