package cli

import (
	"github.com/spf13/cobra"

	"github.com/evil8io/tailjump/internal/platform"
	"github.com/evil8io/tailjump/internal/session"
)

// newLogsCmd is tj logs. It works with no active session: it shows the log
// of the last session, which is the main use after a connect failure.
func newLogsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "logs",
		Short: "Print the log of the active or the last session",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			lines, _ := cmd.Flags().GetInt("lines")
			follow, _ := cmd.Flags().GetBool("follow")
			return session.Logs(cmd.Context(), cmd.OutOrStdout(), platform.LogOptions{
				Lines:  lines,
				Follow: follow,
			})
		},
	}
	cmd.Flags().IntP("lines", "n", 100, "the number of lines to print, 0 for all")
	cmd.Flags().BoolP("follow", "f", false, "keep printing new lines until interrupted")
	return cmd
}
