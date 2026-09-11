package cli

import (
	"errors"
	"fmt"
	"net/netip"
	"os"

	"github.com/spf13/cobra"

	"github.com/evil8io/tailjump/internal/config"
	"github.com/evil8io/tailjump/internal/discovery"
	"github.com/evil8io/tailjump/internal/dns"
	"github.com/evil8io/tailjump/internal/helper"
	"github.com/evil8io/tailjump/internal/manifest"
	"github.com/evil8io/tailjump/internal/session"
	"github.com/evil8io/tailjump/internal/sshc"
)

// exitActiveSession is the exit code connect returns when a session is
// already active. See docs/architecture.md, "CLI conventions".
const exitActiveSession = 3

func newConnectCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "connect <remote>",
		Short: "Start a session into the remote's network",
		Args:  cobra.ExactArgs(1),
		RunE:  runConnect,
	}
	cmd.Flags().String("user", "", "the SSH user")
	cmd.Flags().String("dns", "", "the DNS mode: none, split, or all")
	cmd.Flags().StringArray("exclude", nil, "a CIDR to exclude from the session, repeatable")
	cmd.Flags().Bool("no-discovery", false, "skip discovery")
	cmd.Flags().Bool("replace", false, "end the active session first")
	return cmd
}

func runConnect(cmd *cobra.Command, args []string) error {
	flagUser, _ := cmd.Flags().GetString("user")
	dnsFlag, _ := cmd.Flags().GetString("dns")
	excludeFlags, _ := cmd.Flags().GetStringArray("exclude")
	noDiscovery, _ := cmd.Flags().GetBool("no-discovery")
	replace, _ := cmd.Flags().GetBool("replace")

	if dnsFlag != "" && !dns.Valid(dnsFlag) {
		return fmt.Errorf("invalid --dns %q, want none, split, or all", dnsFlag)
	}

	ctx := cmd.Context()
	cfg, err := loadLocalConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	rr, err := resolveRemote(ctx, newTailnetClient(), cfg, args[0], flagUser)
	if err != nil {
		return err
	}

	client, err := sshc.Dial(ctx, rr.Addr, rr.Peer.HostName, rr.User, knownHostsCacheDir())
	if err != nil {
		return fmt.Errorf("ssh dial %s: %w", rr.Peer.HostName, err)
	}
	defer func() { _ = client.Close() }()

	plan, err := buildPlan(client, rr, cfg, args[0], dnsFlag, excludeFlags, noDiscovery)
	if err != nil {
		return err
	}

	err = session.Connect(ctx, plan, replace, false)
	var ae *session.ActiveError
	if errors.As(err, &ae) {
		fmt.Fprintln(os.Stderr, "Error:", ae)
		os.Exit(exitActiveSession)
	}
	return err
}

// buildPlan runs discovery, computes the session networks, resolves the DNS
// mode, and picks the helper architecture. Discovery always runs, because the
// helper architecture comes from it; --no-discovery drops only the
// discovery-sourced networks.
func buildPlan(client *sshc.Client, rr *resolvedRemote, cfg *config.Config, ref, dnsFlag string, excludeFlags []string, noDiscovery bool) (*session.Plan, error) {
	res, err := discovery.Run(func(script string) ([]byte, error) {
		return client.Run("sh", []byte(script))
	})
	if err != nil {
		return nil, fmt.Errorf("discovery: %w", err)
	}

	m := manifest.Empty()
	body, err := res.DecodedManifest()
	if err != nil {
		return nil, fmt.Errorf("decode manifest: %w", err)
	}
	if len(body) > 0 {
		if m, err = manifest.Parse(body); err != nil {
			return nil, fmt.Errorf("parse manifest: %w", err)
		}
	}

	networks, err := connectNetworks(m, res, cfg, rr, excludeFlags, noDiscovery)
	if err != nil {
		return nil, err
	}
	if len(networks) == 0 {
		return nil, errors.New("the session network list is empty; nothing to route")
	}

	goarch, err := helper.ArchForUname(res.UnameM)
	if err != nil {
		return nil, err
	}

	mode, servers, domains, err := resolveDNS(dnsFlag, cfg, ref, m, res)
	if err != nil {
		return nil, err
	}

	return &session.Plan{
		Remote:     rr.Peer.HostName,
		Addr:       rr.Addr.String(),
		User:       rr.User,
		Networks:   prefixStrings(networks),
		DNS:        session.PlanDNS{Mode: string(mode), Servers: servers, Domains: domains},
		HelperArch: goarch,
	}, nil
}

func connectNetworks(m *manifest.Manifest, res *discovery.Result, cfg *config.Config, rr *resolvedRemote, excludeFlags []string, noDiscovery bool) ([]netip.Prefix, error) {
	manifestNetworks, err := manifest.ParsePrefixes(m.Networks)
	if err != nil {
		return nil, fmt.Errorf("manifest networks: %w", err)
	}
	manifestExclude, err := manifest.ParsePrefixes(m.Exclude)
	if err != nil {
		return nil, fmt.Errorf("manifest exclude: %w", err)
	}
	localExclude, err := manifest.ParsePrefixes(append(append([]string{}, cfg.Exclude...), excludeFlags...))
	if err != nil {
		return nil, fmt.Errorf("exclude: %w", err)
	}

	var linkRoutes, cloudNets []netip.Prefix
	if !noDiscovery {
		if m.LinkRoutesEnabled() {
			if linkRoutes, err = res.LinkRoutePrefixes(); err != nil {
				return nil, fmt.Errorf("discovery link routes: %w", err)
			}
		}
		if m.CloudEnabled() {
			if cloudNets, err = res.CloudNetworkPrefixes(); err != nil {
				return nil, fmt.Errorf("discovery cloud networks: %w", err)
			}
		}
	}

	return manifest.ComputeNetworks(manifest.Inputs{
		ManifestNetworks:    manifestNetworks,
		ManifestExclude:     manifestExclude,
		DiscoveryLinkRoutes: linkRoutes,
		DiscoveryCloud:      cloudNets,
		RemoteAddrs:         rr.Peer.TailscaleIPs,
		LaptopConnected:     laptopConnected(),
		LocalExclude:        localExclude,
	})
}

// resolveDNS picks the mode by precedence (flag, remote config, defaults,
// then the manifest default), and the servers and domains. The servers
// default to the discovered resolvers, the domains to the manifest domains.
func resolveDNS(dnsFlag string, cfg *config.Config, ref string, m *manifest.Manifest, res *discovery.Result) (dns.Mode, []string, []string, error) {
	var domains []string
	if m.DNS != nil {
		domains = m.DNS.Domains
	}
	mode := dnsMode(dnsFlag, cfg, ref, len(domains) > 0)

	var servers []string
	if m.DNS != nil && len(m.DNS.Servers) > 0 {
		servers = m.DNS.Servers
	} else {
		servers = res.Resolvers
	}

	if mode == dns.ModeSplit && len(domains) == 0 {
		return "", nil, nil, errors.New("split DNS needs the manifest dns.domains; set it or use --dns none or all")
	}
	return mode, servers, domains, nil
}

func dnsMode(dnsFlag string, cfg *config.Config, ref string, hasDomains bool) dns.Mode {
	if dnsFlag != "" {
		return dns.Mode(dnsFlag)
	}
	if rc, ok := cfg.Remotes[ref]; ok && rc.DNS != "" {
		return dns.Mode(rc.DNS)
	}
	if cfg.Defaults.DNS != "" {
		return dns.Mode(cfg.Defaults.DNS)
	}
	return dns.Default(hasDomains)
}
