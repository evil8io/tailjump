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

// aliasFields are the fields tj alias unset accepts. host is not among
// them, because an alias needs it.
var aliasFields = []string{"user", "dns", "transport", "protocols", "networks", "exclude", "reconnect_for"}

// newAliasCmd is the tj alias command group. It manages the config aliases
// under remotes. It is a separate command from the hidden _remote helper.
func newAliasCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:        "alias",
		Short:      "Manage the config aliases for remotes",
		Long:       "tj alias manages the aliases in the config file, which connect and describe expand to a host.\n\n" + configLong,
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
		newAliasUnsetCmd(),
		newAliasRmCmd(),
	)
	return cmd
}

func newAliasListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List the config aliases",
		Long: `tj alias list prints every alias, with its host, user, DNS mode, transport, protocols, networks, and exclude list.
ls is an alias for this command.`,
		Args: cobra.NoArgs,
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
		Use:               "show <alias>",
		Short:             "Show one config alias",
		Long:              "tj alias show <alias> prints the host, the user, and the other fields of one config alias.",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeAlias,
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
		Long: `tj alias add <alias> --host <host> creates a new alias in the config file.
--dns, --transport, --protocols, --reconnect-for, --network, and --exclude set the alias fields that connect uses before the config defaults.`,
		Example: `  tj alias add gw --host gw.example
  tj alias add gw --host gw.example --dns split
  tj alias add gw --host gw.example --network 10.0.0.0/16`,
		Args: cobra.ExactArgs(1),
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
		Long: `tj alias set <alias> updates the fields of an alias that tj alias add already created.
The flags replace only the fields you give.
--network and --exclude replace the whole list, not add to it.`,
		Example: `  tj alias set gw --dns split
  tj alias set gw --transport quic --protocols tcp,udp
  tj alias set gw --network 10.0.0.0/16`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeAlias,
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

func newAliasUnsetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unset <alias> <field>...",
		Short: "Clear one or more fields of a config alias",
		Long: `tj alias unset <alias> <field>... clears one or more fields: user, dns, transport, protocols, networks, exclude, or reconnect_for.
host is not a valid field, because an alias needs it.`,
		Args:              cobra.MinimumNArgs(2),
		ValidArgsFunction: completeAliasUnset,
		RunE: func(cmd *cobra.Command, args []string) error {
			alias, fields := args[0], args[1:]
			path := localConfigPath()
			cfg, err := config.Load(path)
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			rc, ok := cfg.Remotes[alias]
			if !ok {
				return fmt.Errorf("remote %q is not in the config", alias)
			}
			for _, field := range fields {
				switch field {
				case "user":
					rc.User = ""
				case "dns":
					rc.DNS = ""
				case "transport":
					rc.Transport = ""
				case "protocols":
					rc.Protocols = ""
				case "networks":
					rc.Networks = nil
				case "exclude":
					rc.Exclude = nil
				case "reconnect_for":
					rc.ReconnectFor = ""
				default:
					return &ExitError{Code: 2, Err: fmt.Errorf("unknown field %q; want %s", field, strings.Join(aliasFields, ", "))}
				}
			}
			cfg.Remotes[alias] = rc
			if err := saveLocalConfig(path, cfg); err != nil {
				return fmt.Errorf("save config: %w", err)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "updated remote %q\n", alias)
			return nil
		},
	}
}

func newAliasRmCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "remove <alias>",
		Aliases: []string{"rm"},
		Short:   "Remove a config alias",
		Long: `tj alias remove <alias> deletes the alias from the config file.
rm is an alias for this command.`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeAlias,
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
	cmd.Flags().Var(&reconnectForValue{}, "reconnect-for", "the reconnect window after a session loss")
	cmd.Flags().StringArray("network", nil, "a CIDR to route for this remote, repeatable; replaces the whole list")
	cmd.Flags().StringArray("exclude", nil, "a CIDR to exclude for this remote, repeatable; replaces the whole list")
	registerRemoteFlagCompletions(cmd)
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
	if f.Changed("reconnect-for") {
		rc.ReconnectFor = flagString(cmd, "reconnect-for")
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
