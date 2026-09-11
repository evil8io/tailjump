package session

import (
	"context"
	"fmt"
	"os"

	"github.com/evil8io/tailjump/internal/platform"
)

// Disconnect ends the active session. When the effective uid is 0 it stops
// the unit directly; otherwise it re-execs through sudo to the root copy. It
// is not an error to disconnect with no active session.
func Disconnect(ctx context.Context) error {
	plat := platform.New()
	active, err := plat.Runner.Active()
	if err != nil {
		return fmt.Errorf("check active session: %w", err)
	}
	if !active {
		_, _ = fmt.Fprintln(os.Stdout, "no active session")
		return nil
	}
	if os.Geteuid() == 0 {
		return Stop()
	}
	return runSudo(ctx, nil, "_session", "stop")
}
