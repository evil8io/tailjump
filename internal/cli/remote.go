package cli

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/evil8io/tailjump/internal/helper"
)

// newRemoteCmd is the remote helper subcommand (tj _remote). It is hidden
// because an engineer never runs it by hand; the client uploads the tj-helper
// binary and runs it. It serves the mux over stdin and stdout.
func newRemoteCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "_remote",
		Short:  "Serve the mux protocol over stdin and stdout",
		Hidden: true,
		RunE: func(_ *cobra.Command, _ []string) error {
			return helper.Run(os.Stdin, os.Stdout, os.Stderr)
		},
	}
}
