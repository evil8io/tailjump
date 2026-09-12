package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/evil8io/tailjump/internal/platform"
)

// Active returns the state of the active session, or nil when none is
// active. It cross-checks the unit, so a stale state file from a crash does
// not report a session that is gone. It reads the state file without root.
func Active() (*State, error) {
	plat := platform.New()
	st, err := ReadState(StatePath(plat.Paths.RuntimeDir()))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	switch active, aerr := plat.Runner.Active(); {
	case aerr != nil:
		slog.Warn("check session active", "error", aerr)
	case !active:
		return nil, nil
	}
	return st, nil
}

// Status prints the active session.
func Status(w io.Writer, asJSON bool) error {
	st, err := Active()
	if err != nil {
		return err
	}
	if st == nil {
		return printNoSession(w, asJSON)
	}

	if asJSON {
		return json.NewEncoder(w).Encode(st)
	}
	return printState(w, st)
}

func printNoSession(w io.Writer, asJSON bool) error {
	if asJSON {
		return json.NewEncoder(w).Encode(map[string]string{"status": "none"})
	}
	_, err := fmt.Fprintln(w, "no active session")
	return err
}

func printState(w io.Writer, st *State) error {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "Remote:\t%s (%s)\n", st.Remote, st.Addr)
	_, _ = fmt.Fprintf(tw, "User:\t%s\n", st.User)
	_, _ = fmt.Fprintf(tw, "Status:\t%s\n", st.Status)
	_, _ = fmt.Fprintf(tw, "Transport:\t%s\n", transportLine(st))
	_, _ = fmt.Fprintf(tw, "DNS mode:\t%s\n", st.DNS.Mode)
	_, _ = fmt.Fprintf(tw, "Uptime:\t%s\n", uptime(st.StartedAt))
	_, _ = fmt.Fprintf(tw, "Networks:\t%s\n", joinOrNone(st.Networks))
	return tw.Flush()
}

func transportLine(st *State) string {
	switch st.Transport {
	case TransportQUIC:
		return fmt.Sprintf("quic (port %d)", st.QUICPort)
	case TransportSSH:
		if st.Fallback != "" {
			return "ssh (fallback: " + st.Fallback + ")"
		}
		return "ssh"
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

func joinOrNone(items []string) string {
	if len(items) == 0 {
		return "(none)"
	}
	return strings.Join(items, ", ")
}
