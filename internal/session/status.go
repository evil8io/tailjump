package session

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
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

// TransportLine names the transport of the session in one line, with the QUIC
// port, the fallback reason, or the lane list. tj status and the final line of
// tj connect print it.
func (s *State) TransportLine() string {
	switch s.Transport {
	case TransportQUIC:
		return fmt.Sprintf("quic (port %d)", s.QUICPort)
	case TransportSSH:
		line := "ssh"
		if s.Fallback != "" {
			line += " (fallback: " + s.Fallback + ")"
		}
		if s.Lanes != "" {
			line += ", lanes " + s.Lanes
		}
		return line
	default:
		return "unknown"
	}
}

// Uptime is the time since the session started, rounded to a second.
func (s *State) Uptime() string {
	t, err := time.Parse(time.RFC3339, s.StartedAt)
	if err != nil {
		return "unknown"
	}
	return time.Since(t).Round(time.Second).String()
}
