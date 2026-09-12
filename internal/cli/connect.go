package cli

import (
	"errors"
	"fmt"
	"log/slog"
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
	"github.com/evil8io/tailjump/internal/transport"
)

// exitActiveSession is the exit code connect returns when a session is
// already active. See docs/architecture.md, "CLI conventions".
const exitActiveSession = 3

func newConnectCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "connect <remote>",
		Aliases: []string{"up"},
		Short:   "Start a session into the remote's network",
		Args:    cobra.ExactArgs(1),
		RunE:    runConnect,
	}
	cmd.Flags().String("user", "", "the SSH user")
	cmd.Flags().String("dns", "", "the DNS mode: none, split, or all")
	cmd.Flags().String("transport", "", "the data plane transport: auto, quic, or ssh")
	cmd.Flags().StringArray("network", nil, "an extra CIDR to route, on top of the manifest and discovery, repeatable")
	cmd.Flags().StringArray("exclude", nil, "a CIDR to exclude from the session, repeatable")
	cmd.Flags().Bool("no-discovery", false, "skip discovery")
	cmd.Flags().Bool("replace", false, "end the active session first")
	return cmd
}

func runConnect(cmd *cobra.Command, args []string) error {
	flagUser, _ := cmd.Flags().GetString("user")
	dnsFlag, _ := cmd.Flags().GetString("dns")
	transportFlag, _ := cmd.Flags().GetString("transport")
	networkFlags, _ := cmd.Flags().GetStringArray("network")
	excludeFlags, _ := cmd.Flags().GetStringArray("exclude")
	noDiscovery, _ := cmd.Flags().GetBool("no-discovery")
	replace, _ := cmd.Flags().GetBool("replace")

	if dnsFlag != "" && !dns.Valid(dnsFlag) {
		return fmt.Errorf("invalid --dns %q, want none, split, or all", dnsFlag)
	}
	if transportFlag != "" && !transport.Valid(transportFlag) {
		return fmt.Errorf("invalid --transport %q, want auto, quic, or ssh", transportFlag)
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

	client, err := dialRemote(ctx, rr.Addr, rr.Peer.HostName, rr.User)
	if err != nil {
		return fmt.Errorf("ssh dial %s: %w", rr.Peer.HostName, err)
	}
	defer func() { _ = client.Close() }()

	plan, err := buildPlan(client, rr, cfg, args[0], dnsFlag, transportFlag, networkFlags, excludeFlags, noDiscovery)
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
func buildPlan(client *sshc.Client, rr *resolvedRemote, cfg *config.Config, ref, dnsFlag, transportFlag string, networkFlags, excludeFlags []string, noDiscovery bool) (*session.Plan, error) {
	res, err := runDiscovery(client)
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

	networks, err := connectNetworks(m, res, cfg, rr, networkFlags, excludeFlags, noDiscovery)
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
	ports, err := m.QUICPorts()
	if err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	up, down, err := m.Bandwidth()
	if err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	tmode := transportMode(transportFlag, cfg, ref)
	controller, err := controllerKnob()
	if err != nil {
		return nil, err
	}
	slog.Debug("session networks", "count", len(networks), "networks", prefixStrings(networks), "dns", mode, "transport", tmode)

	return &session.Plan{
		Remote:        rr.Peer.HostName,
		Addr:          rr.Addr.String(),
		User:          rr.User,
		Networks:      prefixStrings(networks),
		DNS:           session.PlanDNS{Mode: string(mode), Servers: servers, Domains: domains},
		HelperArch:    goarch,
		Transport:     string(tmode),
		QUICPorts:     ports.String(),
		BandwidthUp:   up,
		BandwidthDown: down,
		Controller:    controller,
	}, nil
}

// controllerKnob reads the measurement knob TJ_QUIC_CONTROLLER. Only cubic
// is valid; it selects the library default on both sides for a comparison
// run. See docs/architecture.md, "Testing".
func controllerKnob() (string, error) {
	v := os.Getenv("TJ_QUIC_CONTROLLER")
	switch v {
	case "", transport.Cubic:
		return v, nil
	default:
		return "", fmt.Errorf("invalid TJ_QUIC_CONTROLLER %q, want cubic or unset", v)
	}
}

// transportMode picks the transport by precedence: the flag, then the remote
// config, then the defaults, then auto.
func transportMode(flag string, cfg *config.Config, ref string) transport.Mode {
	return transport.Resolve(flag, cfg.Remotes[ref].Transport, cfg.Defaults.Transport)
}

func connectNetworks(m *manifest.Manifest, res *discovery.Result, cfg *config.Config, rr *resolvedRemote, networkFlags, excludeFlags []string, noDiscovery bool) ([]netip.Prefix, error) {
	// Include the remote manifest and discovery, plus the remote-config and
	// flag networks; exclude the manifest, config, remote-config, and flag
	// excludes.
	includeNetworks := append(append([]string{}, m.Networks...), rr.Config.Networks...)
	includeNetworks = append(includeNetworks, networkFlags...)
	manifestNetworks, err := manifest.ParsePrefixes(includeNetworks)
	if err != nil {
		return nil, fmt.Errorf("networks: %w", err)
	}
	manifestExclude, err := manifest.ParsePrefixes(m.Exclude)
	if err != nil {
		return nil, fmt.Errorf("manifest exclude: %w", err)
	}
	excludeList := append(append([]string{}, cfg.Exclude...), rr.Config.Exclude...)
	excludeList = append(excludeList, excludeFlags...)
	localExclude, err := manifest.ParsePrefixes(excludeList)
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
		ClientConnected:     clientConnected(),
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
