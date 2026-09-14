package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/evil8io/tailjump/internal/config"
	"github.com/evil8io/tailjump/internal/manifest"
)

// configKeys are the keys tj config set and tj config unset accept.
var configKeys = []string{"defaults.user", "defaults.dns", "defaults.transport", "defaults.protocols", "exclude"}

// configLong is the tj config and tj alias Long text. A CLI write of either
// command group replaces the whole config file, so it drops the comments of
// a file an engineer edited by hand; editing the file directly keeps them,
// and the next tj command that loads the file validates it.
const configLong = `A CLI write removes the comments in the config file.
Edit the file by hand to keep the comments.
Run $EDITOR $(tj config path) to open it.
The next tj command validates the file.`

// newConfigCmd is the tj config command group. It reads and writes the
// defaults and the global exclude list, not the per-remote aliases; those
// belong to tj alias.
func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage the tj defaults",
		Long:  "Manage the tj defaults and the global exclude list.\n\n" + configLong,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(
		newConfigPathCmd(),
		newConfigGetCmd(),
		newConfigSetCmd(),
		newConfigUnsetCmd(),
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
	Protocols string   `json:"protocols"`
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
					Protocols: cfg.Defaults.Protocols,
					Exclude:   cfg.Exclude,
				})
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			_, _ = fmt.Fprintf(w, "defaults.user:\t%s\n", valueOrDash(cfg.Defaults.User))
			_, _ = fmt.Fprintf(w, "defaults.dns:\t%s\n", valueOrDash(cfg.Defaults.DNS))
			_, _ = fmt.Fprintf(w, "defaults.transport:\t%s\n", valueOrDash(cfg.Defaults.Transport))
			_, _ = fmt.Fprintf(w, "defaults.protocols:\t%s\n", valueOrDash(cfg.Defaults.Protocols))
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
		Short: "Set defaults.user, defaults.dns, defaults.transport, defaults.protocols, or exclude",
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
				var v dnsModeValue
				if err := v.Set(value); err != nil {
					return fmt.Errorf("invalid dns %q, %w", value, err)
				}
				cfg.Defaults.DNS = value
			case "defaults.transport":
				var v transportModeValue
				if err := v.Set(value); err != nil {
					return fmt.Errorf("invalid transport %q, %w", value, err)
				}
				cfg.Defaults.Transport = value
			case "defaults.protocols":
				var v protocolSetValue
				if err := v.Set(value); err != nil {
					return fmt.Errorf("invalid protocols %q, %w", value, err)
				}
				cfg.Defaults.Protocols = value
			case "exclude":
				cidrs, err := parseCIDRList(value)
				if err != nil {
					return fmt.Errorf("invalid exclude %q: %w", value, err)
				}
				cfg.Exclude = cidrs
			default:
				return unknownConfigKey(key)
			}
			if err := saveLocalConfig(path, cfg); err != nil {
				return fmt.Errorf("save config: %w", err)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "set %s\n", key)
			return nil
		},
	}
}

func newConfigUnsetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unset <key>",
		Short: "Clear defaults.user, defaults.dns, defaults.transport, defaults.protocols, or exclude",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			key := args[0]
			path := localConfigPath()
			cfg, err := config.Load(path)
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			switch key {
			case "defaults.user":
				cfg.Defaults.User = ""
			case "defaults.dns":
				cfg.Defaults.DNS = ""
			case "defaults.transport":
				cfg.Defaults.Transport = ""
			case "defaults.protocols":
				cfg.Defaults.Protocols = ""
			case "exclude":
				cfg.Exclude = nil
			default:
				return unknownConfigKey(key)
			}
			if err := saveLocalConfig(path, cfg); err != nil {
				return fmt.Errorf("save config: %w", err)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "unset %s\n", key)
			return nil
		},
	}
}

// unknownConfigKey is the ExitError of an unknown tj config set or tj
// config unset key. See docs/architecture.md, "CLI conventions".
func unknownConfigKey(key string) error {
	return &ExitError{Code: 2, Err: fmt.Errorf("unknown key %q; want %s", key, strings.Join(configKeys, ", "))}
}

// parseCIDRList splits a comma-separated CIDR list and validates every
// entry with manifest.ParsePrefixes.
func parseCIDRList(value string) ([]string, error) {
	items := strings.Split(value, ",")
	for i, item := range items {
		items[i] = strings.TrimSpace(item)
	}
	if _, err := manifest.ParsePrefixes(items); err != nil {
		return nil, err
	}
	return items, nil
}
