// Package cli has the tj cobra commands. Every command except version
// returns the error "not implemented" until the chunk that owns it fills
// in the body.
package cli

import (
	"errors"
	"log/slog"
	"os"

	"github.com/spf13/cobra"
)

var errNotImplemented = errors.New("not implemented")

// Execute runs the tj root command.
func Execute() error {
	return newRootCmd().Execute()
}

func newRootCmd() *cobra.Command {
	var verbose bool

	root := &cobra.Command{
		Use:           "tj",
		Short:         "tj gives an engineer a session into a remote network over Tailscale SSH",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRun: func(_ *cobra.Command, _ []string) {
			setupLogging(verbose)
		},
	}
	root.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "enable debug logging")

	root.AddCommand(
		newVersionCmd(),
		newSetupCmd(),
		newListCmd(),
		newDescribeCmd(),
		newDoctorCmd(),
		newConnectCmd(),
		newDisconnectCmd(),
		newStatusCmd(),
		newRemoteCmd(),
		newSessionCmd(),
	)

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

func runNotImplemented(_ *cobra.Command, _ []string) error {
	return errNotImplemented
}
