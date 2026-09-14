package cli

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/evil8io/tailjump/internal/config"
	"github.com/evil8io/tailjump/internal/manifest"
)

// newAliasCmd is the tj alias command group. It manages the config aliases
// under remotes. It is a separate command from the hidden _remote helper.
func newAliasCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:        "alias",
		Short:      "Manage the config aliases for remotes",
		Args:       cobra.NoArgs,
		SuggestFor: []string{"remote"},
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(
		newAliasListCmd(),
		newAliasShowCmd(),
		newAliasAddCmd(),
		newAliasSetCmd(),
		newAliasRmCmd(),
	)
	return cmd
}

func newAliasListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List the config aliases",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			asJSON, _ := cmd.Flags().GetBool("json")
			cfg, err := config.Load(localConfigPath())
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			if asJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(cfg.Remotes)
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			_, _ = fmt.Fprintln(w, "ALIAS\tHOST\tUSER\tDNS\tTRANSPORT\tPROTOCOLS\tNETWORKS\tEXCLUDE")
			for _, alias := range sortedRemoteAliases(cfg) {
				rc := cfg.Remotes[alias]
				_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
					alias,
					valueOrDash(rc.Host),
					valueOrDash(rc.User),
					valueOrDash(rc.DNS),
					valueOrDash(rc.Transport),
					valueOrDash(rc.Protocols),
					joinOrDash(rc.Networks),
					joinOrDash(rc.Exclude),
				)
			}
			return w.Flush()
		},
	}
	cmd.Flags().Bool("json", false, "print JSON output")
	return cmd
}

func newAliasShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <alias>",
		Short: "Show one config alias",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			asJSON, _ := cmd.Flags().GetBool("json")
			cfg, err := config.Load(localConfigPath())
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			rc, ok := cfg.Remotes[args[0]]
			if !ok {
				return fmt.Errorf("remote %q is not in the config", args[0])
			}
			if asJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(rc)
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			_, _ = fmt.Fprintf(w, "Alias:\t%s\n", args[0])
			_, _ = fmt.Fprintf(w, "Host:\t%s\n", valueOrDash(rc.Host))
			_, _ = fmt.Fprintf(w, "User:\t%s\n", valueOrDash(rc.User))
			_, _ = fmt.Fprintf(w, "DNS:\t%s\n", valueOrDash(rc.DNS))
			_, _ = fmt.Fprintf(w, "Transport:\t%s\n", valueOrDash(rc.Transport))
			_, _ = fmt.Fprintf(w, "Protocols:\t%s\n", valueOrDash(rc.Protocols))
			_, _ = fmt.Fprintf(w, "Networks:\t%s\n", joinOrDash(rc.Networks))
			_, _ = fmt.Fprintf(w, "Exclude:\t%s\n", joinOrDash(rc.Exclude))
			return w.Flush()
		},
	}
	cmd.Flags().Bool("json", false, "print JSON output")
	return cmd
}

func newAliasAddCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "add <alias>",
		Short: "Add a config alias",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			alias := args[0]
			path := localConfigPath()
			cfg, err := config.Load(path)
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			if _, ok := cfg.Remotes[alias]; ok {
				return fmt.Errorf("remote %q already exists; use tj alias set to change it", alias)
			}
			rc, err := remoteFromFlags(cmd, config.RemoteConfig{})
			if err != nil {
				return err
			}
			if cfg.Remotes == nil {
				cfg.Remotes = map[string]config.RemoteConfig{}
			}
			cfg.Remotes[alias] = rc
			if err := saveLocalConfig(path, cfg); err != nil {
				return fmt.Errorf("save config: %w", err)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "added remote %q\n", alias)
			return nil
		},
	}
	addRemoteFlags(cmd)
	_ = cmd.MarkFlagRequired("host")
	return cmd
}

func newAliasSetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "set <alias>",
		Short: "Update an existing config alias",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			alias := args[0]
			path := localConfigPath()
			cfg, err := config.Load(path)
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			rc, ok := cfg.Remotes[alias]
			if !ok {
				return fmt.Errorf("remote %q is not in the config", alias)
			}
			rc, err = remoteFromFlags(cmd, rc)
			if err != nil {
				return err
			}
			cfg.Remotes[alias] = rc
			if err := saveLocalConfig(path, cfg); err != nil {
				return fmt.Errorf("save config: %w", err)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "updated remote %q\n", alias)
			return nil
		},
	}
	addRemoteFlags(cmd)
	return cmd
}

func newAliasRmCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "remove <alias>",
		Aliases: []string{"rm"},
		Short:   "Remove a config alias",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			alias := args[0]
			path := localConfigPath()
			cfg, err := config.Load(path)
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			if _, ok := cfg.Remotes[alias]; !ok {
				return fmt.Errorf("remote %q is not in the config", alias)
			}
			delete(cfg.Remotes, alias)
			if err := saveLocalConfig(path, cfg); err != nil {
				return fmt.Errorf("save config: %w", err)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "removed remote %q\n", alias)
			return nil
		},
	}
}

func addRemoteFlags(cmd *cobra.Command) {
	cmd.Flags().String("host", "", "the remote host")
	cmd.Flags().String("user", "", "the SSH user")
	cmd.Flags().Var(&dnsModeValue{}, "dns", "the DNS mode")
	cmd.Flags().Var(&transportModeValue{}, "transport", "the data plane transport")
	cmd.Flags().Var(&protocolSetValue{}, "protocols", "the protocols to forward")
	cmd.Flags().StringArray("network", nil, "a CIDR to route for this remote, repeatable")
	cmd.Flags().StringArray("exclude", nil, "a CIDR to exclude for this remote, repeatable")
}

// remoteFromFlags returns base with the fields whose flags were set on cmd
// overwritten. pflag validates the DNS mode, the transport, and the
// protocol set at parse time; this function still validates every CIDR, so
// a bad one fails before the config is written.
func remoteFromFlags(cmd *cobra.Command, base config.RemoteConfig) (config.RemoteConfig, error) {
	rc := base
	f := cmd.Flags()
	if f.Changed("host") {
		rc.Host, _ = f.GetString("host")
	}
	if f.Changed("user") {
		rc.User, _ = f.GetString("user")
	}
	if f.Changed("dns") {
		rc.DNS = flagString(cmd, "dns")
	}
	if f.Changed("transport") {
		rc.Transport = flagString(cmd, "transport")
	}
	if f.Changed("protocols") {
		rc.Protocols = flagString(cmd, "protocols")
	}
	if f.Changed("network") {
		v, _ := f.GetStringArray("network")
		if _, err := manifest.ParsePrefixes(v); err != nil {
			return rc, fmt.Errorf("--network: %w", err)
		}
		rc.Networks = v
	}
	if f.Changed("exclude") {
		v, _ := f.GetStringArray("exclude")
		if _, err := manifest.ParsePrefixes(v); err != nil {
			return rc, fmt.Errorf("--exclude: %w", err)
		}
		rc.Exclude = v
	}
	return rc, nil
}

func sortedRemoteAliases(cfg *config.Config) []string {
	aliases := make([]string, 0, len(cfg.Remotes))
	for a := range cfg.Remotes {
		aliases = append(aliases, a)
	}
	sort.Strings(aliases)
	return aliases
}

func valueOrDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func joinOrDash(items []string) string {
	if len(items) == 0 {
		return "-"
	}
	return strings.Join(items, ",")
}
