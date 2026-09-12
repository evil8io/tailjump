package session

import (
	"errors"
	"log/slog"
	"os"

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
