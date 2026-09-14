package session

import (
	"context"
	"io"
	"os"
	"strconv"
	"time"

	"github.com/evil8io/tailjump/internal/platform"
)

// Logs writes the session log to out. When the effective uid is 0 it reads
// the log directly through Runner.Logs; otherwise it re-execs through sudo to
// the root copy, because an unprivileged user cannot read the session log on
// either platform.
func Logs(ctx context.Context, out, errw io.Writer, opts platform.LogOptions) error {
	if os.Geteuid() == 0 {
		return platform.New().Runner.Logs(ctx, out, opts)
	}
	if err := CheckRootCopy(ctx); err != nil {
		return err
	}
	args := []string{"_session", "logs", "-n", strconv.Itoa(opts.Lines)}
	if opts.Follow {
		args = append(args, "-f")
	}
	if !opts.Since.IsZero() {
		args = append(args, "--since", opts.Since.Format(time.RFC3339))
	}
	return runSudo(ctx, out, errw, nil, args...)
}
