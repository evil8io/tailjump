package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/evil8io/tailjump/internal/session"
	"github.com/evil8io/tailjump/internal/transport"
)

func newStatusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "status",
		Aliases: []string{"st"},
		Short:   "Print the active session",
		Long: `tj status prints the active session: the remote, the transport, the DNS mode, and the uptime.
It also prints the latency of the tailscale path, the round-trip time of the transport, and the traffic.
While the session is up it sends disco pings to the remote through the local API of tailscaled.
It reads the local state file and needs no root privilege.
With no active session, it prints that fact and exits 0.`,
		Args: cobra.NoArgs,
		RunE: runStatus,
	}
	cmd.Flags().Bool("json", false, "print JSON output")
	return cmd
}

// statusJSON is the state file plus the Tailscale path to the remote.
type statusJSON struct {
	*session.State
	Path *pathInfo `json:"path,omitempty"`
}

func runStatus(cmd *cobra.Command, _ []string) error {
	asJSON, _ := cmd.Flags().GetBool("json")
	w := cmd.OutOrStdout()

	st, err := session.Active()
	if err != nil {
		return err
	}
	if st == nil {
		if asJSON {
			return json.NewEncoder(w).Encode(map[string]string{"status": "none"})
		}
		_, err := fmt.Fprintln(w, "no active session")
		return err
	}

	path := sessionPath(cmd.Context(), st)
	if asJSON {
		return json.NewEncoder(w).Encode(statusJSON{State: st, Path: path})
	}
	return writeStatus(w, st, path)
}

// writeStatus prints the rows of the session. The metrics rows are absent
// when the state file has no metrics, for example one an older session
// wrote.
func writeStatus(w io.Writer, st *session.State, path *pathInfo) error {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "Remote:\t%s (%s)\n", st.Remote, st.Addr)
	_, _ = fmt.Fprintf(tw, "User:\t%s\n", st.User)
	_, _ = fmt.Fprintf(tw, "Status:\t%s\n", st.StatusLine())
	_, _ = fmt.Fprintf(tw, "Transport:\t%s\n", st.TransportLine())
	_, _ = fmt.Fprintf(tw, "Path:\t%s\n", path)
	if m := st.Metrics; m != nil {
		if m.RTTMS > 0 {
			_, _ = fmt.Fprintf(tw, "RTT:\t%s\n", formatRTT(m.RTTMS))
		}
		_, _ = fmt.Fprintf(tw, "Traffic:\t%s\n", trafficLine(m))
	}
	_, _ = fmt.Fprintf(tw, "Protocols:\t%s\n", valueOrDash(st.Protocols))
	_, _ = fmt.Fprintf(tw, "DNS mode:\t%s\n", st.DNS.Mode)
	_, _ = fmt.Fprintf(tw, "Uptime:\t%s\n", st.Uptime())
	if st.Reconnects > 0 {
		_, _ = fmt.Fprintf(tw, "Reconnects:\t%d\n", st.Reconnects)
	}
	_, _ = fmt.Fprintf(tw, "Networks:\t%s\n", joinOrNone(st.Networks))
	return tw.Flush()
}

// formatRTT prints a round-trip time in milliseconds. A value below 10 keeps
// one decimal.
func formatRTT(ms float64) string {
	if ms < 10 {
		return fmt.Sprintf("%.1fms", ms)
	}
	return fmt.Sprintf("%.0fms", ms)
}

// trafficLine is the rates and the totals of the session, for example
// "up 1200 kbps, down 8400 kbps (310 MiB up, 2.1 GiB down)".
func trafficLine(m *session.Metrics) string {
	return fmt.Sprintf("up %s, down %s (%s up, %s down)",
		transport.FormatRate(m.UpRate), transport.FormatRate(m.DownRate),
		formatBytes(m.UpBytes), formatBytes(m.DownBytes))
}

// sessionPath reads tailscaled's path to the session's remote with the rule
// of tj list. A session that is up gets a disco ping, because an idle path
// has no latency and the session proves the peer answers. A local API error
// leaves the path unknown.
func sessionPath(ctx context.Context, st *session.State) *pathInfo {
	tc := newTailnetClient()
	tst, err := tc.Status(ctx)
	if err != nil {
		return nil
	}
	for _, p := range tst.Peers {
		for _, a := range p.TailscaleIPs {
			if a.String() != st.Addr {
				continue
			}
			if st.Status == session.StatusUp {
				if pi := pingPath(ctx, tc, a); pi != nil {
					return pi
				}
			}
			return pathOf(p)
		}
	}
	return nil
}
