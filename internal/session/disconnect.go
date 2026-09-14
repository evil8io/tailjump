package session

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/evil8io/tailjump/internal/platform"
)

// Disconnect ends the active session. When the effective uid is 0 it stops
// the unit directly; otherwise it re-execs through sudo to the root copy. It
// is not an error to disconnect with no active session.
func Disconnect(ctx context.Context, out, errw io.Writer) error {
	plat := platform.New()
	active, err := plat.Runner.Active()
	if err != nil {
		return fmt.Errorf("check active session: %w", err)
	}
	if !active {
		_, err := fmt.Fprintln(out, "no active session")
		return err
	}

	// The stop removes the state file, so the remote is read before it.
	remote := ""
	if st, serr := ReadState(StatePath(plat.Paths.RuntimeDir())); serr == nil {
		remote = st.Remote
	}

	if os.Geteuid() == 0 {
		err = Stop()
	} else {
		err = runSudo(ctx, out, errw, nil, "_session", "stop")
	}
	if err != nil {
		return err
	}
	if remote == "" {
		_, err = fmt.Fprintln(out, "session ended")
		return err
	}
	_, err = fmt.Fprintf(out, "session to %s ended\n", remote)
	return err
}
