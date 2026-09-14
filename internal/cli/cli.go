// Package cli has the tj cobra commands.
package cli

import (
	"errors"
	"log/slog"
	"os"

	"github.com/spf13/cobra"

	"github.com/evil8io/tailjump/internal/version"
)

// ExitError is an error with an explicit process exit code. main prints its
// message and exits with Code. See docs/architecture.md, "CLI conventions".
type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string { return e.Err.Error() }
func (e *ExitError) Unwrap() error { return e.Err }

// Execute runs the tj root command.
func Execute() error {
	return newRootCmd().Execute()
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
