package cli

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/evil8io/tailjump/internal/config"
	"github.com/evil8io/tailjump/internal/discovery"
	"github.com/evil8io/tailjump/internal/dns"
	"github.com/evil8io/tailjump/internal/helper"
	"github.com/evil8io/tailjump/internal/manifest"
	"github.com/evil8io/tailjump/internal/protocols"
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
	cmd.Flags().Var(&dnsModeValue{}, "dns", "the DNS mode")
	cmd.Flags().Var(&transportModeValue{}, "transport", "the data plane transport")
	cmd.Flags().Var(&protocolSetValue{}, "protocols", "the protocols to forward")
	cmd.Flags().StringArray("network", nil, "an extra CIDR to route, on top of the manifest and discovery, repeatable")
	cmd.Flags().StringArray("exclude", nil, "a CIDR to exclude from the session, repeatable")
	cmd.Flags().Bool("no-discovery", false, "skip discovery")
	cmd.Flags().Bool("replace", false, "end the active session first")
	cmd.Flags().Bool("dry-run", false, "print the plan and exit, without a session")
	cmd.Flags().Bool("json", false, "print the plan as JSON; needs --dry-run")
	return cmd
}

func runConnect(cmd *cobra.Command, args []string) error {
	flagUser, _ := cmd.Flags().GetString("user")
	dnsFlag := flagString(cmd, "dns")
	transportFlag := flagString(cmd, "transport")
	protocolsFlag := flagString(cmd, "protocols")
	networkFlags, _ := cmd.Flags().GetStringArray("network")
	excludeFlags, _ := cmd.Flags().GetStringArray("exclude")
	noDiscovery, _ := cmd.Flags().GetBool("no-discovery")
	replace, _ := cmd.Flags().GetBool("replace")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	asJSON, _ := cmd.Flags().GetBool("json")
	if asJSON && !dryRun {
		return &ExitError{Code: 2, Err: errors.New("--json needs --dry-run")}
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

	plan, err := buildPlan(client, rr, cfg, args[0], planFlags{
		dns:       dnsFlag,
		transport: transportFlag,
		protocols: protocolsFlag,
		networks:  networkFlags,
		excludes:  excludeFlags,
	}, noDiscovery)
	if err != nil {
		return err
	}

	if dryRun {
		return printPlan(cmd, plan, asJSON)
	}

	err = session.Connect(ctx, plan, replace, false)
	var ae *session.ActiveError
	if errors.As(err, &ae) {
		return &ExitError{Code: exitActiveSession, Err: ae}
	}
	return err
}

// printPlan prints the plan that connect would hand to _session start. It
// does not start a session. The human layout matches describe: asJSON
// prints the exact bytes _session start reads from stdin.
func printPlan(cmd *cobra.Command, plan *session.Plan, asJSON bool) error {
	if asJSON {
		b, err := plan.Marshal()
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), string(b))
		return err
	}

	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintf(w, "Remote:\t%s (%s)\n", plan.Remote, plan.Addr)
	_, _ = fmt.Fprintf(w, "User:\t%s\n", plan.User)
	_, _ = fmt.Fprintf(w, "Transport:\t%s\n", plan.Transport)
	_, _ = fmt.Fprintf(w, "QUIC ports:\t%s\n", plan.QUICPorts)
	_, _ = fmt.Fprintf(w, "Protocols:\t%s\n", plan.Protocols)
	_, _ = fmt.Fprintf(w, "DNS mode:\t%s\n", plan.DNS.Mode)
	_, _ = fmt.Fprintf(w, "DNS servers:\t%s\n", joinOrNone(plan.DNS.Servers))
	_, _ = fmt.Fprintf(w, "DNS domains:\t%s\n", joinOrNone(plan.DNS.Domains))
	_, _ = fmt.Fprintf(w, "Helper arch:\t%s\n", plan.HelperArch)
	_, _ = fmt.Fprintln(w, "Networks:")
	for _, n := range plan.Networks {
		_, _ = fmt.Fprintf(w, "  %s\n", n)
	}
	return w.Flush()
}

// planFlags are the connect flags that shape the plan.
type planFlags struct {
	dns       string
	transport string
	protocols string
	networks  []string
	excludes  []string
}

// buildPlan runs discovery, computes the session networks, resolves the DNS
// mode and the protocol set, and picks the helper architecture. Discovery
// always runs, because the helper architecture comes from it;
// --no-discovery drops only the discovery-sourced networks.
func buildPlan(client *sshc.Client, rr *resolvedRemote, cfg *config.Config, ref string, flags planFlags, noDiscovery bool) (*session.Plan, error) {
	res, err := runDiscovery(client)
	if err != nil {
		return nil, fmt.Errorf("discovery: %w", err)
	}

	body, err := res.DecodedManifest()
	if err != nil {
		return nil, fmt.Errorf("decode manifest: %w", err)
	}
	m, err := decodeManifest(body)
	if err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}

	discForNetworks := res
	if noDiscovery {
		discForNetworks = nil
	}
	networks, _, err := sessionNetworks(m, discForNetworks, cfg, rr, flags.networks, flags.excludes)
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

	mode, servers, domains, err := resolveDNS(flags.dns, cfg, ref, m, res)
	if err != nil {
		return nil, err
	}
	set, err := protocols.Resolve(flags.protocols, cfg.Remotes[ref].Protocols, cfg.Defaults.Protocols)
	if err != nil {
		return nil, err
	}
	if err := checkDNSProtocols(mode, set); err != nil {
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
	tmode := transportMode(flags.transport, cfg, ref)
	controller, err := controllerKnob()
	if err != nil {
		return nil, err
	}
	singleLane, err := singleLaneKnob()
	if err != nil {
		return nil, err
	}
	slog.Debug("session networks", "count", len(networks), "networks", prefixStrings(networks), "dns", mode, "transport", tmode, "protocols", set)

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
		Protocols:     set.String(),
		SingleLane:    singleLane,
	}, nil
}

// checkDNSProtocols refuses a DNS mode that needs the tunnel when the set
// has no udp, because the split and all modes send the queries through it.
func checkDNSProtocols(mode dns.Mode, set protocols.Set) error {
	if mode == dns.ModeNone || set.UDP {
		return nil
	}
	return fmt.Errorf("--dns %s sends the DNS queries through the tunnel, but the protocol set %q has no udp; use --dns none or add udp", mode, set)
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

// singleLaneKnob reads the measurement knob TJ_SSH_LANES. Only 1 is valid;
// it keeps the SSH transport on the primary lane alone, so a run compares
// with and without the lanes. The session runs as root in a transient unit
// and does not inherit this environment, so the plan carries the value. See
// docs/architecture.md, "Testing".
func singleLaneKnob() (bool, error) {
	switch v := os.Getenv("TJ_SSH_LANES"); v {
	case "":
		return false, nil
	case "1":
		return true, nil
	default:
		return false, fmt.Errorf("invalid TJ_SSH_LANES %q, want 1 or unset", v)
	}
}

// transportMode picks the transport by precedence: the flag, then the remote
// config, then the defaults, then auto.
func transportMode(flag string, cfg *config.Config, ref string) transport.Mode {
	return transport.Resolve(flag, cfg.Remotes[ref].Transport, cfg.Defaults.Transport)
}

// resolveDNS picks the mode by precedence (flag, remote config, defaults,
// then the manifest default), and the servers and domains. The servers
// default to the discovered resolvers, the domains to the manifest domains.
// A nil res means no discovery ran, so the servers come from the manifest
// only.
func resolveDNS(dnsFlag string, cfg *config.Config, ref string, m *manifest.Manifest, res *discovery.Result) (dns.Mode, []string, []string, error) {
	var domains []string
	if m.DNS != nil {
		domains = m.DNS.Domains
	}
	mode := dnsMode(dnsFlag, cfg, ref, len(domains) > 0)

	var servers []string
	if m.DNS != nil && len(m.DNS.Servers) > 0 {
		servers = m.DNS.Servers
	} else if res != nil {
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
