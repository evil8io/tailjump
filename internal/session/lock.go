package session

import (
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/evil8io/tailjump/internal/platform"
)

// ActiveError reports that a session is already active. connect returns it to
// exit with code 3.
type ActiveError struct {
	Remote    string
	StartedAt string
}

func (e *ActiveError) Error() string {
	if e.Remote == "" {
		return "a session is already active; use --replace to end it first"
	}
	if e.StartedAt != "" {
		return fmt.Sprintf("a session to %s is active since %s; use --replace to end it first", e.Remote, e.StartedAt)
	}
	return fmt.Sprintf("a session to %s is active; use --replace to end it first", e.Remote)
}

// activeSession reports whether a session is active and, when it can, names
// the remote and start time from the state file. The unit is the authority;
// the state file is only for the message.
func activeSession(plat platform.Platform) (bool, *ActiveError, error) {
	active, err := plat.Runner.Active()
	if err != nil {
		return false, nil, err
	}
	if !active {
		return false, nil, nil
	}
	ae := &ActiveError{}
	switch st, err := ReadState(StatePath(plat.Paths.RuntimeDir())); {
	case err == nil:
		ae.Remote = st.Remote
		ae.StartedAt = st.StartedAt
	case errors.Is(err, os.ErrNotExist):
		// The unit is active but has not written the state file yet.
	default:
		slog.Warn("read session state", "error", err)
	}
	return true, ae, nil
}
