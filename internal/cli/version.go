package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/evil8io/tailjump/internal/version"
)

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the tj version",
		Long: `Print the tj version string.
Build tooling sets it from the git tag at release.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprintln(cmd.OutOrStdout(), version.Version)
			return err
		},
	}
}
