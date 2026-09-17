package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/evil8io/tailjump/internal/helper"
	"github.com/evil8io/tailjump/internal/mux"
	"github.com/evil8io/tailjump/internal/session"
	"github.com/evil8io/tailjump/internal/tailnet"
	"github.com/evil8io/tailjump/internal/transport"
)

// The --duration range. The helper accepts 1 s to 30 s in whole seconds.
const (
	benchMinDuration     = time.Second
	benchMaxDuration     = 30 * time.Second
	benchDefaultDuration = 5 * time.Second
)

func newBenchCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bench <remote>",
		Short: "Measure the throughput of the transport to the remote",
		Long: `tj bench <remote> measures the throughput between the client and a temporary helper on the remote.
It measures the transport, not the TUN data plane of a session.
It needs no session and no root, and it changes no active session.
The congestion controller is BBR in both directions, so the result describes the path.
The command sends for the duration up, then for the duration down, so it loads the remote for twice that time.
An active session to the same remote shares the path and lowers the result.
The two rates are the values for transport.bandwidth in the remote's manifest.`,
		Example: `  tj bench gw.example
  tj bench gw.example --duration 10s
  tj bench tag:example --transport ssh --json`,
		Args:              cobra.ExactArgs(1),
		RunE:              runBench,
		ValidArgsFunction: completeRemote,
	}
	cmd.Flags().String("user", "", "the SSH user")
	cmd.Flags().Var(&transportModeValue{}, "transport", "the data plane transport")
	cmd.Flags().Duration("duration", benchDefaultDuration, "the time of each direction, 1s to 30s")
	cmd.Flags().Bool("json", false, "print JSON output")
	_ = cmd.RegisterFlagCompletionFunc("user", cobra.NoFileCompletions)
	_ = cmd.RegisterFlagCompletionFunc("transport", completeTransportMode)
	_ = cmd.RegisterFlagCompletionFunc("duration", cobra.NoFileCompletions)
	return cmd
}

// benchLeg is one measured direction. Rate is in bytes per second, the unit
// of the plan's bandwidth_up.
type benchLeg struct {
	Bytes   uint64  `json:"bytes"`
	Seconds float64 `json:"seconds"`
	Rate    uint64  `json:"rate"`
}

// benchOutput is the tj bench report, for the human table and for --json.
type benchOutput struct {
	Remote    string    `json:"remote"`
	Addr      string    `json:"addr"`
	Transport string    `json:"transport"`
	QUICPort  uint16    `json:"quic_port,omitempty"`
	Fallback  string    `json:"fallback,omitempty"`
	Path      *pathInfo `json:"path"`
	Up        benchLeg  `json:"up"`
	Down      benchLeg  `json:"down"`
}

func runBench(cmd *cobra.Command, args []string) error {
	asJSON, _ := cmd.Flags().GetBool("json")
	flagUser, _ := cmd.Flags().GetString("user")
	transportFlag := flagString(cmd, "transport")
	duration, _ := cmd.Flags().GetDuration("duration")
	if err := checkBenchDuration(duration); err != nil {
		return &ExitError{Code: 2, Err: err}
	}

	ctx := cmd.Context()
	cfg, err := loadLocalConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	tc := newTailnetClient()
	rr, err := resolveRemote(ctx, tc, cfg, args[0], flagUser)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "bench %s: %s up, then %s down\n", rr.Peer.HostName, duration, duration)

	client, err := dialRemote(ctx, rr.Addr, rr.Peer.HostName, rr.User)
	if err != nil {
		return fmt.Errorf("ssh dial %s: %w", rr.Peer.HostName, err)
	}
	defer func() { _ = client.Close() }()

	res, err := runDiscovery(client)
	if err != nil {
		return fmt.Errorf("discovery: %w", err)
	}
	arch, err := helper.ArchForUname(res.UnameM)
	if err != nil {
		return err
	}
	body, err := res.DecodedManifest()
	if err != nil {
		return fmt.Errorf("decode manifest: %w", err)
	}
	m, err := decodeManifest(body)
	if err != nil {
		return fmt.Errorf("parse manifest: %w", err)
	}
	ports, err := m.QUICPorts()
	if err != nil {
		return fmt.Errorf("manifest: %w", err)
	}

	path := benchPath(ctx, tc, rr)
	report, err := session.Bench(ctx, client, arch, rr.Addr, transportMode(transportFlag, cfg, args[0]), ports, duration)
	if err != nil {
		return err
	}
	return printBench(cmd, benchReport(rr, path, report), asJSON)
}

// checkBenchDuration accepts the range the helper accepts.
func checkBenchDuration(d time.Duration) error {
	if d < benchMinDuration || d > benchMaxDuration || d%time.Second != 0 {
		return fmt.Errorf("invalid --duration %s, want %s to %s in whole seconds", d, benchMinDuration, benchMaxDuration)
	}
	return nil
}

// benchPath is the Tailscale path to the peer, measured before the bench
// loads it. A peer that answers no ping keeps the path of the tailnet
// status.
func benchPath(ctx context.Context, tc *tailnet.Client, rr *resolvedRemote) *pathInfo {
	if pi := pingPath(ctx, tc, rr.Addr); pi != nil {
		return pi
	}
	return pathOf(rr.Peer)
}

func benchReport(rr *resolvedRemote, path *pathInfo, report session.BenchReport) benchOutput {
	return benchOutput{
		Remote:    rr.Peer.HostName,
		Addr:      rr.Addr.String(),
		Transport: report.Transport,
		QUICPort:  report.QUICPort,
		Fallback:  report.Fallback,
		Path:      path,
		Up:        benchLegOf(report.Up),
		Down:      benchLegOf(report.Down),
	}
}

func benchLegOf(r mux.BenchResult) benchLeg {
	return benchLeg{Bytes: r.Bytes, Seconds: r.Elapsed.Seconds(), Rate: r.Rate()}
}

func printBench(cmd *cobra.Command, out benchOutput, asJSON bool) error {
	if asJSON {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(out)
	}
	st := session.State{Transport: out.Transport, QUICPort: out.QUICPort, Fallback: out.Fallback}
	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintf(w, "Remote:\t%s (%s)\n", out.Remote, out.Addr)
	_, _ = fmt.Fprintf(w, "Transport:\t%s\n", st.TransportLine())
	_, _ = fmt.Fprintf(w, "Path:\t%s\n", out.Path)
	_, _ = fmt.Fprintf(w, "Up:\t%s\n", benchLine(out.Up))
	_, _ = fmt.Fprintf(w, "Down:\t%s\n", benchLine(out.Down))
	return w.Flush()
}

// benchLine is one direction of the human table, for example
// "212 mbps (127 MiB in 5.0s)".
func benchLine(leg benchLeg) string {
	return fmt.Sprintf("%s (%s in %.1fs)", transport.FormatRate(leg.Rate), formatBytes(leg.Bytes), leg.Seconds)
}
