package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/netip"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/evil8io/tailjump/internal/config"
	"github.com/evil8io/tailjump/internal/discovery"
	"github.com/evil8io/tailjump/internal/dns"
	"github.com/evil8io/tailjump/internal/helper"
	"github.com/evil8io/tailjump/internal/manifest"
	"github.com/evil8io/tailjump/internal/platform"
	"github.com/evil8io/tailjump/internal/session"
	"github.com/evil8io/tailjump/internal/sshc"
)

func newDoctorCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "doctor <remote>",
		Short: "Run the manifest checks from the remote and from the client",
		Args:  cobra.ExactArgs(1),
		RunE:  runDoctor,
	}
	cmd.Flags().String("user", "", "the SSH user")
	cmd.Flags().Bool("json", false, "print JSON output")
	return cmd
}

// doctorCheck is one line of tj doctor output: a fact or a pass/fail
// result. Value carries the fact for an informational check and "ok" or
// "fail: <reason>" for a pass/fail one.
type doctorCheck struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

func runDoctor(cmd *cobra.Command, args []string) error {
	asJSON, _ := cmd.Flags().GetBool("json")
	flagUser, _ := cmd.Flags().GetString("user")
	ctx := cmd.Context()

	cfg, err := loadLocalConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	var checks []doctorCheck
	add := func(name, value string) { checks = append(checks, doctorCheck{Name: name, Value: value}) }
	addErr := func(name string, err error) {
		if err != nil {
			add(name, "fail: "+err.Error())
		} else {
			add(name, "ok")
		}
	}

	rr, err := resolveRemote(ctx, newTailnetClient(), cfg, args[0], flagUser)
	addErr("peer online", err)
	if err != nil {
		return printDoctor(cmd, checks, asJSON)
	}
	add("peer", fmt.Sprintf("%s (%s)", rr.Peer.HostName, rr.Addr))

	client, err := dialRemote(ctx, rr.Addr, rr.Peer.HostName, rr.User)
	addErr("ssh ok", err)
	if err != nil {
		return printDoctor(cmd, checks, asJSON)
	}
	defer func() { _ = client.Close() }()
	add("banner", "printed to stderr, if the remote sent one")

	res, err := runDiscovery(client)
	addErr("discovery ok", err)
	if err != nil {
		return printDoctor(cmd, checks, asJSON)
	}
	add("manifest", valueOrAbsent(res.ManifestPath))
	add("exec dir", valueOrAbsent(res.ExecDir))

	m, err := decodeDoctorManifest(res)
	addErr("parse manifest", err)
	if err != nil {
		return printDoctor(cmd, checks, asJSON)
	}

	networks, err := doctorSessionNetworks(m, res, cfg, rr)
	if err == nil && len(networks) == 0 {
		err = fmt.Errorf("the session network list is empty")
	}
	addErr("session networks non-empty", err)
	if err == nil {
		add("session networks", joinOrNone(prefixStrings(networks)))
	}

	if platform.New().Resolver.Available() {
		add("dns mode availability", "resolved available: split and all both work")
	} else {
		add("dns mode availability", "resolved not available: only all (resolv.conf fallback) works, split needs resolved")
	}
	add("dns default mode", string(dns.Default(m.DNS != nil && len(m.DNS.Domains) > 0)))

	add("remote manifest checks", "pending (needs the helper)")

	probe, err := doctorProbe(ctx, client, res, m, rr)
	switch {
	case err != nil:
		add("quic transport", "fail: "+err.Error())
		add("icmp echo socket", "fail: "+err.Error())
	default:
		if probe.QUICErr != nil {
			add("quic transport", "fail: "+probe.QUICErr.Error())
		} else {
			add("quic transport", fmt.Sprintf("ok, port %d", probe.QUICPort))
		}
		if probe.EchoErr != nil {
			add("icmp echo socket", "fail: "+probe.EchoErr.Error())
		} else {
			add("icmp echo socket", probe.Echo.String())
		}
	}

	return printDoctor(cmd, checks, asJSON)
}

// doctorProbe brings the QUIC transport up through a temporary helper and
// tears it down, so the report shows whether a UDP packet reaches the range
// on the remote, and asks the helper which socket it has for ICMP echo.
func doctorProbe(ctx context.Context, client *sshc.Client, res *discovery.Result, m *manifest.Manifest, rr *resolvedRemote) (session.ProbeResult, error) {
	goarch, err := helper.ArchForUname(res.UnameM)
	if err != nil {
		return session.ProbeResult{}, err
	}
	ports, err := m.QUICPorts()
	if err != nil {
		return session.ProbeResult{}, err
	}
	up, down, err := m.Bandwidth()
	if err != nil {
		return session.ProbeResult{}, err
	}
	return session.Probe(ctx, client, goarch, rr.Addr, ports, up, down)
}

func decodeDoctorManifest(res *discovery.Result) (*manifest.Manifest, error) {
	body, err := res.DecodedManifest()
	if err != nil {
		return nil, err
	}
	if len(body) == 0 {
		return manifest.Empty(), nil
	}
	return manifest.Parse(body)
}

// doctorSessionNetworks mirrors buildDescribeOutput's network computation,
// so tj doctor and tj describe report the same session networks for the
// same remote.
func doctorSessionNetworks(m *manifest.Manifest, res *discovery.Result, cfg *config.Config, rr *resolvedRemote) ([]netip.Prefix, error) {
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

	networks, err := manifest.ComputeNetworks(manifest.Inputs{
		ManifestNetworks:    manifestNetworks,
		ManifestExclude:     manifestExclude,
		DiscoveryLinkRoutes: linkRoutes,
		DiscoveryCloud:      cloudNets,
		RemoteAddrs:         rr.Peer.TailscaleIPs,
		ClientConnected:     clientConnected(),
		LocalExclude:        localExclude,
	})
	if err != nil {
		return nil, err
	}
	slog.Debug("session networks", "count", len(networks), "networks", prefixStrings(networks))
	return networks, nil
}

func valueOrAbsent(s string) string {
	if s == "" {
		return "absent"
	}
	return s
}

func printDoctor(cmd *cobra.Command, checks []doctorCheck, asJSON bool) error {
	if asJSON {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(checks)
	}
	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
	for _, c := range checks {
		_, _ = fmt.Fprintf(w, "%s:\t%s\n", c.Name, c.Value)
	}
	return w.Flush()
}
