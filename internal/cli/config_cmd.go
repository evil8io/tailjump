package cli

import (
	"encoding/json"
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/evil8io/tailjump/internal/config"
	"github.com/evil8io/tailjump/internal/dns"
	"github.com/evil8io/tailjump/internal/transport"
)

// newConfigCmd is the tj config command group. It reads and writes the
// defaults and the global exclude list, not the per-remote aliases; those
// belong to tj remote.
func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage the tj defaults",
	}
	cmd.AddCommand(
		newConfigPathCmd(),
		newConfigGetCmd(),
		newConfigSetCmd(),
	)
	return cmd
}

func newConfigPathCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "path",
		Short: "Print the resolved config file path",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprintln(cmd.OutOrStdout(), localConfigPath())
			return err
		},
	}
}

type configGetOutput struct {
	User      string   `json:"user"`
	DNS       string   `json:"dns"`
	Transport string   `json:"transport"`
	Exclude   []string `json:"exclude,omitempty"`
}

func newConfigGetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get",
		Short: "Print the defaults and the global exclude list",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			asJSON, _ := cmd.Flags().GetBool("json")
			cfg, err := config.Load(localConfigPath())
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			if asJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(configGetOutput{
					User:      cfg.Defaults.User,
					DNS:       cfg.Defaults.DNS,
					Transport: cfg.Defaults.Transport,
					Exclude:   cfg.Exclude,
				})
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			_, _ = fmt.Fprintf(w, "defaults.user:\t%s\n", valueOrDash(cfg.Defaults.User))
			_, _ = fmt.Fprintf(w, "defaults.dns:\t%s\n", valueOrDash(cfg.Defaults.DNS))
			_, _ = fmt.Fprintf(w, "defaults.transport:\t%s\n", valueOrDash(cfg.Defaults.Transport))
			_, _ = fmt.Fprintf(w, "exclude:\t%s\n", joinOrDash(cfg.Exclude))
			return w.Flush()
		},
	}
	cmd.Flags().Bool("json", false, "print JSON output")
	return cmd
}

func newConfigSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set <key> <value>",
		Short: "Set defaults.user, defaults.dns, or defaults.transport",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			key, value := args[0], args[1]
			path := localConfigPath()
			cfg, err := config.Load(path)
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			switch key {
			case "defaults.user":
				cfg.Defaults.User = value
			case "defaults.dns":
				if !dns.Valid(value) {
					return fmt.Errorf("invalid dns %q, want none, split, or all", value)
				}
				cfg.Defaults.DNS = value
			case "defaults.transport":
				if !transport.Valid(value) {
					return fmt.Errorf("invalid transport %q, want auto, quic, or ssh", value)
				}
				cfg.Defaults.Transport = value
			default:
				return fmt.Errorf("unknown key %q; set defaults.user, defaults.dns, or defaults.transport", key)
			}
			if err := saveLocalConfig(path, cfg); err != nil {
				return fmt.Errorf("save config: %w", err)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "set %s\n", key)
			return nil
		},
	}
}
