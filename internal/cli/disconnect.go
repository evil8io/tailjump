package cli

import (
	"github.com/spf13/cobra"

	"github.com/evil8io/tailjump/internal/session"
)

func newDisconnectCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "disconnect",
		Aliases: []string{"down"},
		Short:   "End the active session",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return session.Disconnect(cmd.Context())
		},
	}
}
