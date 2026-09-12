package cli

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/netip"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/evil8io/tailjump/internal/config"
	"github.com/evil8io/tailjump/internal/discovery"
	"github.com/evil8io/tailjump/internal/manifest"
	"github.com/evil8io/tailjump/internal/protocols"
	"github.com/evil8io/tailjump/internal/sshc"
)

func newDescribeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "describe <remote>",
		Aliases: []string{"desc"},
		Short:   "Print the merged config for a remote without a session",
		Args:    cobra.ExactArgs(1),
		RunE:    runDescribe,
	}
	cmd.Flags().String("user", "", "the SSH user")
	cmd.Flags().Bool("no-discovery", false, "skip discovery")
	cmd.Flags().Bool("json", false, "print JSON output")
	return cmd
}

// describeExclusions is every source manifest.ComputeNetworks subtracts,
// kept separate for display: describe prints each exclusion, not only the
// final network list.
type describeExclusions struct {
	ManifestExclude []string `json:"manifest_exclude"`
	Reserved        []string `json:"reserved"`
	RemoteAddrs     []string `json:"remote_addrs"`
	ClientConnected []string `json:"client_connected"`
	LocalExclude    []string `json:"local_exclude"`
}

type describeOutput struct {
	Remote         string             `json:"remote"`
	Addr           string             `json:"addr"`
	User           string             `json:"user"`
	Protocols      string             `json:"protocols"`
	ManifestSource string             `json:"manifest_source"`
	Manifest       *manifest.Manifest `json:"manifest"`
	Discovery      *discovery.Result  `json:"discovery,omitempty"`
	Exclusions     describeExclusions `json:"exclusions"`
	Networks       []string           `json:"networks"`
}

func runDescribe(cmd *cobra.Command, args []string) error {
	flagUser, _ := cmd.Flags().GetString("user")
	noDiscovery, _ := cmd.Flags().GetBool("no-discovery")
	asJSON, _ := cmd.Flags().GetBool("json")

	ctx := cmd.Context()
	cfg, err := loadLocalConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	rr, err := resolveRemote(ctx, newTailnetClient(), cfg, args[0], flagUser)
	if err != nil {
		return err
	}

	client, err := dialRemote(ctx, rr.Addr, rr.Peer.HostName, rr.User)
	if err != nil {
		return fmt.Errorf("ssh dial %s: %w", rr.Peer.HostName, err)
	}
	defer func() { _ = client.Close() }()

	out, err := buildDescribeOutput(client, rr, cfg, noDiscovery)
	if err != nil {
		return err
	}

	if asJSON {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(out)
	}
	return printDescribe(cmd, out)
}

// buildDescribeOutput reads the manifest, runs discovery unless
// noDiscovery, and computes the session networks. See
// docs/architecture.md, "Session networks".
func buildDescribeOutput(client *sshc.Client, rr *resolvedRemote, cfg *config.Config, noDiscovery bool) (*describeOutput, error) {
	var (
		manifestPath string
		manifestBody []byte
		discRes      *discovery.Result
	)

	if noDiscovery {
		p, body, err := fetchManifestOnly(client)
		if err != nil {
			return nil, err
		}
		manifestPath, manifestBody = p, body
	} else {
		res, err := runDiscovery(client)
		if err != nil {
			return nil, fmt.Errorf("discovery: %w", err)
		}
		discRes = res
		manifestPath = res.ManifestPath
		manifestBody, err = res.DecodedManifest()
		if err != nil {
			return nil, fmt.Errorf("decode manifest: %w", err)
		}
	}

	m := manifest.Empty()
	if len(manifestBody) > 0 {
		parsed, err := manifest.Parse(manifestBody)
		if err != nil {
			return nil, fmt.Errorf("parse manifest: %w", err)
		}
		m = parsed
	}

	// Include the remote manifest and discovery, plus the remote-config
	// networks; exclude the manifest, config, and remote-config excludes.
	includeNetworks := append(append([]string{}, m.Networks...), rr.Config.Networks...)
	manifestNetworks, err := manifest.ParsePrefixes(includeNetworks)
	if err != nil {
		return nil, fmt.Errorf("networks: %w", err)
	}
	manifestExclude, err := manifest.ParsePrefixes(m.Exclude)
	if err != nil {
		return nil, fmt.Errorf("manifest exclude: %w", err)
	}
	localExcludeList := append(append([]string{}, cfg.Exclude...), rr.Config.Exclude...)
	localExclude, err := manifest.ParsePrefixes(localExcludeList)
	if err != nil {
		return nil, fmt.Errorf("local config exclude: %w", err)
	}

	var linkRoutes, cloudNets []netip.Prefix
	if discRes != nil {
		if m.LinkRoutesEnabled() {
			if linkRoutes, err = discRes.LinkRoutePrefixes(); err != nil {
				return nil, fmt.Errorf("discovery link routes: %w", err)
			}
		}
		if m.CloudEnabled() {
			if cloudNets, err = discRes.CloudNetworkPrefixes(); err != nil {
				return nil, fmt.Errorf("discovery cloud networks: %w", err)
			}
		}
	}

	connected := clientConnected()

	networks, err := manifest.ComputeNetworks(manifest.Inputs{
		ManifestNetworks:    manifestNetworks,
		ManifestExclude:     manifestExclude,
		DiscoveryLinkRoutes: linkRoutes,
		DiscoveryCloud:      cloudNets,
		RemoteAddrs:         rr.Peer.TailscaleIPs,
		ClientConnected:     connected,
		LocalExclude:        localExclude,
	})
	if err != nil {
		return nil, fmt.Errorf("compute session networks: %w", err)
	}
	slog.Debug("session networks", "count", len(networks), "networks", prefixStrings(networks))

	source := manifestPath
	if source == "" {
		source = "none"
	}
	set, err := protocols.Resolve("", rr.Config.Protocols, cfg.Defaults.Protocols)
	if err != nil {
		return nil, err
	}

	return &describeOutput{
		Remote:         rr.Peer.HostName,
		Addr:           rr.Addr.String(),
		User:           rr.User,
		Protocols:      set.String(),
		ManifestSource: source,
		Manifest:       m,
		Discovery:      discRes,
		Exclusions: describeExclusions{
			ManifestExclude: m.Exclude,
			Reserved:        reservedForDisplay,
			RemoteAddrs:     addrStrings(rr.Peer.TailscaleIPs),
			ClientConnected: prefixStrings(connected),
			LocalExclude:    localExcludeList,
		},
		Networks: prefixStrings(networks),
	}, nil
}

func printDescribe(cmd *cobra.Command, out *describeOutput) error {
	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)

	_, _ = fmt.Fprintf(w, "Remote:\t%s (%s)\n", out.Remote, out.Addr)
	_, _ = fmt.Fprintf(w, "User:\t%s\n", out.User)
	_, _ = fmt.Fprintf(w, "Protocols:\t%s\n", out.Protocols)
	_, _ = fmt.Fprintf(w, "Manifest:\t%s\n", out.ManifestSource)
	m := out.Manifest
	if m.Name != "" {
		_, _ = fmt.Fprintf(w, "  name:\t%s\n", m.Name)
	}
	if m.Description != "" {
		_, _ = fmt.Fprintf(w, "  description:\t%s\n", m.Description)
	}
	if len(m.Networks) > 0 {
		_, _ = fmt.Fprintf(w, "  networks:\t%s\n", strings.Join(m.Networks, ", "))
	}
	if len(m.Exclude) > 0 {
		_, _ = fmt.Fprintf(w, "  exclude:\t%s\n", strings.Join(m.Exclude, ", "))
	}
	if m.DNS != nil {
		_, _ = fmt.Fprintf(w, "  dns servers:\t%s\n", joinOrNone(m.DNS.Servers))
		_, _ = fmt.Fprintf(w, "  dns domains:\t%s\n", joinOrNone(m.DNS.Domains))
	}
	if len(m.Checks) > 0 {
		checks := make([]string, len(m.Checks))
		for i, c := range m.Checks {
			checks[i] = fmt.Sprintf("%s=%s", c.Name, c.TCP)
		}
		_, _ = fmt.Fprintf(w, "  checks:\t%s\n", strings.Join(checks, ", "))
	}

	d := out.Discovery
	if d == nil {
		_, _ = fmt.Fprintln(w, "Discovery:\tskipped (--no-discovery)")
	} else {
		_, _ = fmt.Fprintln(w, "Discovery:")
		_, _ = fmt.Fprintf(w, "  hostname:\t%s\n", d.Hostname)
		_, _ = fmt.Fprintf(w, "  uname_m:\t%s\n", d.UnameM)
		_, _ = fmt.Fprintf(w, "  exec_dir:\t%s\n", d.ExecDir)
		_, _ = fmt.Fprintf(w, "  addresses:\t%s\n", joinOrNone(d.Addresses))
		_, _ = fmt.Fprintf(w, "  link_routes:\t%s\n", joinOrNone(d.LinkRoutes))
		_, _ = fmt.Fprintf(w, "  resolvers:\t%s\n", joinOrNone(d.Resolvers))
		_, _ = fmt.Fprintf(w, "  search_domains:\t%s\n", joinOrNone(d.SearchDomains))
		if d.Cloud != nil {
			_, _ = fmt.Fprintf(w, "  cloud:\t%s\n", d.Cloud.Provider)
			_, _ = fmt.Fprintf(w, "  cloud_networks:\t%s\n", joinOrNone(d.Cloud.Networks))
		}
	}

	_, _ = fmt.Fprintln(w, "Exclusions:")
	_, _ = fmt.Fprintf(w, "  manifest exclude:\t%s\n", joinOrNone(out.Exclusions.ManifestExclude))
	_, _ = fmt.Fprintf(w, "  reserved:\t%s\n", joinOrNone(out.Exclusions.Reserved))
	_, _ = fmt.Fprintf(w, "  remote address:\t%s\n", joinOrNone(out.Exclusions.RemoteAddrs))
	_, _ = fmt.Fprintf(w, "  client connected:\t%s\n", joinOrNone(out.Exclusions.ClientConnected))
	_, _ = fmt.Fprintf(w, "  local exclude:\t%s\n", joinOrNone(out.Exclusions.LocalExclude))

	_, _ = fmt.Fprintln(w, "Session networks:")
	if len(out.Networks) == 0 {
		_, _ = fmt.Fprintln(w, "  (none)")
	}
	for _, n := range out.Networks {
		_, _ = fmt.Fprintf(w, "  %s\n", n)
	}

	return w.Flush()
}

func joinOrNone(items []string) string {
	if len(items) == 0 {
		return "(none)"
	}
	return strings.Join(items, ", ")
}
