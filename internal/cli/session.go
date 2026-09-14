package cli

import (
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/evil8io/tailjump/internal/platform"
	"github.com/evil8io/tailjump/internal/session"
)

// newSessionCmd is the root-privileged session runner (tj _session). It is
// hidden because an engineer never runs it by hand; tj connect and
// tj disconnect invoke it through sudo. Its subcommands are the session
// lifecycle: start the unit, run the session, stop the unit, and clean up.
func newSessionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "_session",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(
		newSessionStartCmd(),
		newSessionRunCmd(),
		newSessionStopCmd(),
		newSessionCleanupCmd(),
		newSessionLogsCmd(),
	)
	return cmd
}

func newSessionStartCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "start",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			foreground, _ := cmd.Flags().GetBool("foreground")
			planJSON, err := io.ReadAll(cmd.InOrStdin())
			if err != nil {
				return fmt.Errorf("read plan from stdin: %w", err)
			}
			if len(planJSON) == 0 {
				return fmt.Errorf("read plan from stdin: empty plan")
			}
			return session.Start(cmd.Context(), planJSON, foreground)
		},
	}
	cmd.Flags().Bool("foreground", false, "run the session in-process instead of a transient unit")
	return cmd
}

func newSessionRunCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "run <plan>",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return session.Run(cmd.Context(), args[0])
		},
	}
}

func newSessionStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "stop",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return session.Stop()
		},
	}
}

func newSessionCleanupCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "cleanup",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return session.Cleanup()
		},
	}
}

func newSessionLogsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "logs",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			lines, _ := cmd.Flags().GetInt("lines")
			follow, _ := cmd.Flags().GetBool("follow")
			sinceFlag, _ := cmd.Flags().GetString("since")
			var since time.Time
			if sinceFlag != "" {
				t, err := time.Parse(time.RFC3339, sinceFlag)
				if err != nil {
					return fmt.Errorf("invalid --since %q: %w", sinceFlag, err)
				}
				since = t
			}
			return platform.New().Runner.Logs(cmd.Context(), cmd.OutOrStdout(), platform.LogOptions{
				Lines:  lines,
				Follow: follow,
				Since:  since,
			})
		},
	}
	cmd.Flags().IntP("lines", "n", 100, "the number of lines to print, 0 for all")
	cmd.Flags().BoolP("follow", "f", false, "keep printing new lines until interrupted")
	cmd.Flags().String("since", "", "print lines since this RFC 3339 time")
	return cmd
}
