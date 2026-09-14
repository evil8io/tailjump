package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/evil8io/tailjump/internal/session"
)

func newStatusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "status",
		Aliases: []string{"st"},
		Short:   "Print the active session, its transport, its path, its protocols, its networks, the DNS mode, and the uptime",
		Args:    cobra.NoArgs,
		RunE:    runStatus,
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
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "Remote:\t%s (%s)\n", st.Remote, st.Addr)
	_, _ = fmt.Fprintf(tw, "User:\t%s\n", st.User)
	_, _ = fmt.Fprintf(tw, "Status:\t%s\n", st.Status)
	_, _ = fmt.Fprintf(tw, "Transport:\t%s\n", transportLine(st))
	_, _ = fmt.Fprintf(tw, "Path:\t%s\n", path)
	_, _ = fmt.Fprintf(tw, "Protocols:\t%s\n", valueOrDash(st.Protocols))
	_, _ = fmt.Fprintf(tw, "DNS mode:\t%s\n", st.DNS.Mode)
	_, _ = fmt.Fprintf(tw, "Uptime:\t%s\n", uptime(st.StartedAt))
	_, _ = fmt.Fprintf(tw, "Networks:\t%s\n", joinOrNone(st.Networks))
	return tw.Flush()
}

// sessionPath reads tailscaled's path to the session's remote with the rule
// of tj list. A local API error leaves the path unknown.
func sessionPath(ctx context.Context, st *session.State) *pathInfo {
	tst, err := newTailnetClient().Status(ctx)
	if err != nil {
		return nil
	}
	for _, p := range tst.Peers {
		for _, a := range p.TailscaleIPs {
			if a.String() == st.Addr {
				return pathOf(p)
			}
		}
	}
	return nil
}

func transportLine(st *session.State) string {
	switch st.Transport {
	case session.TransportQUIC:
		return fmt.Sprintf("quic (port %d)", st.QUICPort)
	case session.TransportSSH:
		line := "ssh"
		if st.Fallback != "" {
			line += " (fallback: " + st.Fallback + ")"
		}
		if st.Lanes != "" {
			line += ", lanes " + st.Lanes
		}
		return line
	default:
		return "unknown"
	}
}

func uptime(startedAt string) string {
	t, err := time.Parse(time.RFC3339, startedAt)
	if err != nil {
		return "unknown"
	}
	return time.Since(t).Round(time.Second).String()
}
