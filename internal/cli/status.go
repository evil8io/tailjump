package cli

import (
	"github.com/spf13/cobra"

	"github.com/evil8io/tailjump/internal/session"
)

func newStatusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "status",
		Aliases: []string{"st"},
		Short:   "Print the active session, its networks, the DNS mode, and the uptime",
		RunE: func(cmd *cobra.Command, _ []string) error {
			asJSON, _ := cmd.Flags().GetBool("json")
			return session.Status(cmd.OutOrStdout(), asJSON)
		},
	}
	cmd.Flags().Bool("json", false, "print JSON output")
	return cmd
}
