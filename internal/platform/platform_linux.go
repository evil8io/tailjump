//go:build linux

package platform

import (
	"context"
	"io"

	"github.com/evil8io/tailjump/internal/platform/linux"
)

var (
	_ Device   = (*linux.Device)(nil)
	_ Router   = (*linux.Router)(nil)
	_ Resolver = (*linux.Resolver)(nil)
	_ Runner   = linuxRunner{}
	_ Paths    = (*linux.Paths)(nil)
)

func newDevice() Device     { return linux.NewDevice() }
func newRouter() Router     { return linux.NewRouter() }
func newResolver() Resolver { return linux.NewResolver() }
func newRunner() Runner     { return linuxRunner{linux.NewRunner()} }
func newPaths() Paths       { return linux.NewPaths() }

// linuxRunner adapts linux.Runner to Runner. internal/platform/linux cannot
// import this package to spell LogOptions itself, because this package
// already imports internal/platform/linux, and Go refuses the import cycle;
// linux.Runner.Logs takes the options apart into plain arguments instead.
type linuxRunner struct{ *linux.Runner }

func (r linuxRunner) Logs(ctx context.Context, w io.Writer, opts LogOptions) error {
	return r.Runner.Logs(ctx, w, opts.Lines, opts.Since, opts.Follow)
}
