// Package cli has the tj cobra commands.
package cli

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/evil8io/tailjump/internal/session"
	"github.com/evil8io/tailjump/internal/version"
)

// exitInterrupted is the exit code of a command that a signal stopped. See
// docs/architecture.md, "CLI conventions".
const exitInterrupted = 130

// signalExitZero is the annotation of a command that exits 0 after a signal
// instead of exitInterrupted.
const signalExitZero = "tj.signal-exit-zero"

// ExitError is an error with an explicit process exit code. main prints its
// message and exits with Code. See docs/architecture.md, "CLI conventions".
type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string { return e.Err.Error() }
func (e *ExitError) Unwrap() error { return e.Err }

// Execute runs the tj root command with a context that SIGINT and SIGTERM
// cancel. A command that ends while that context is done exits 130, whatever
// it returned. An unprivileged process restores the default signal action
// after the first signal, so a second Ctrl-C ends it at once. The root copy
// keeps its handler, because it stops the session unit after the cancel and a
// second signal would leave that unit running.
func Execute() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if os.Geteuid() != 0 {
		go func() {
			<-ctx.Done()
			stop()
		}()
	}

	cmd, err := newRootCmd().ExecuteContextC(ctx)
	if interrupted(ctx, cmd, err) {
		return &ExitError{Code: exitInterrupted, Err: errors.New("interrupted")}
	}
	return err
}

// interrupted reports whether a signal stopped the command. sudo runs the
// root copy in a pseudo-terminal, so a Ctrl-C during a connect can reach that
// child alone: this process then has a live context and an error that wraps
// session.ErrInterrupted.
func interrupted(ctx context.Context, cmd *cobra.Command, err error) bool {
	if exitsZeroOnSignal(cmd) {
		return false
	}
	return ctx.Err() != nil || errors.Is(err, session.ErrInterrupted)
}

func exitsZeroOnSignal(cmd *cobra.Command) bool {
	return cmd != nil && cmd.Annotations[signalExitZero] != ""
}

func newRootCmd() *cobra.Command {
	var verbose bool

	root := &cobra.Command{
		Use:           "tj",
		Short:         "tj gives an engineer a session into a remote network over Tailscale SSH",
		Version:       version.Version,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(_ *cobra.Command, _ []string) error {
			setupLogging(verbose)
			return nil
		},
	}
	root.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "enable debug logging")
	root.SetVersionTemplate("{{.Version}}\n")

	root.AddCommand(
		newVersionCmd(),
		newSetupCmd(),
		newListCmd(),
		newDescribeCmd(),
		newDoctorCmd(),
		newConnectCmd(),
		newDisconnectCmd(),
		newStatusCmd(),
		newLogsCmd(),
		newAliasCmd(),
		newConfigCmd(),
		newRemoteCmd(),
		newSessionCmd(),
	)
	wrapRunE(root)

	return root
}

func setupLogging(verbose bool) {
	level := slog.LevelInfo
	if verbose {
		level = slog.LevelDebug
	}
	handler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})
	slog.SetDefault(slog.New(handler))
}

// wrapRunE walks the command tree and turns a plain error from a RunE into
// an ExitError with code 1, a runtime error. An error that is already an
// ExitError keeps its code, for example connect's active-session error. See
// docs/architecture.md, "CLI conventions".
func wrapRunE(cmd *cobra.Command) {
	if cmd.RunE != nil {
		run := cmd.RunE
		cmd.RunE = func(c *cobra.Command, args []string) error {
			err := run(c, args)
			if err == nil {
				return nil
			}
			var ee *ExitError
			if errors.As(err, &ee) {
				return err
			}
			return &ExitError{Code: 1, Err: err}
		}
	}
	for _, child := range cmd.Commands() {
		wrapRunE(child)
	}
}
