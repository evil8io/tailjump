package session

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/evil8io/tailjump/internal/platform"
	"github.com/evil8io/tailjump/internal/version"
)

// RootCopy is the root-owned binary that tj setup installs. connect re-execs
// through sudo to it, so the NOPASSWD sudoers rule points at a file the user
// cannot write.
const RootCopy = "/usr/local/libexec/tj/tj"

// sudoWaitDelay is the time the root copy gets to stop the session unit after
// the cancel signal, before Wait kills sudo.
const sudoWaitDelay = 30 * time.Second

// Connect refuses when a session is active, then starts the session. When the
// effective uid is 0 it runs the start in-process; otherwise it re-execs
// through sudo to the root copy. The plan is already computed by the caller.
func Connect(ctx context.Context, plan *Plan, replace, foreground bool) error {
	plat := platform.New()

	active, ae, err := activeSession(plat)
	if err != nil {
		return fmt.Errorf("check active session: %w", err)
	}
	if active {
		if !replace {
			return ae
		}
		if err := stopActive(ctx, plat); err != nil {
			return fmt.Errorf("replace active session: %w", err)
		}
	}

	planJSON, err := plan.Marshal()
	if err != nil {
		return err
	}

	if os.Geteuid() == 0 {
		return Start(ctx, planJSON, foreground)
	}
	return sudoStart(ctx, planJSON, foreground)
}

// stopActive ends the active session and waits until the unit is inactive, so
// a following start does not clash with the old unit name.
func stopActive(ctx context.Context, plat platform.Platform) error {
	if os.Geteuid() == 0 {
		if err := Stop(); err != nil {
			return err
		}
	} else {
		if err := runSudo(ctx, nil, "_session", "stop"); err != nil {
			return err
		}
	}
	return waitInactive(ctx, plat, 25*time.Second)
}

func waitInactive(ctx context.Context, plat platform.Platform, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		active, err := plat.Runner.Active()
		if err != nil {
			return err
		}
		if !active {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the previous session did not stop within %s", timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// sudoStart re-execs the root copy under sudo -n and feeds the plan on stdin.
// It first checks that the root copy version matches, so a stale copy fails
// with a clear message instead of a subtle mismatch.
func sudoStart(ctx context.Context, planJSON []byte, foreground bool) error {
	if err := CheckRootCopy(ctx); err != nil {
		return err
	}
	args := []string{"_session", "start"}
	if foreground {
		args = append(args, "--foreground")
	}
	return runSudo(ctx, planJSON, args...)
}

// CheckRootCopy runs the root copy's version command through sudo -n, so one
// call checks that the file exists, that its version matches this binary,
// and that the sudo rule is in place. sudo -n never prompts.
func CheckRootCopy(ctx context.Context) error {
	if _, err := os.Stat(RootCopy); err != nil {
		return fmt.Errorf("the root copy %s is missing; run tj setup", RootCopy)
	}
	out, err := exec.CommandContext(ctx, "sudo", "-n", RootCopy, "version").Output()
	if err != nil {
		return fmt.Errorf("sudo -n %s version failed: %s; run tj setup", RootCopy, exitStderr(err))
	}
	got := strings.TrimSpace(string(out))
	if got != version.Version {
		return fmt.Errorf("the root copy is version %q, this binary is %q; run tj setup", got, version.Version)
	}
	return nil
}

// exitStderr returns the trimmed stderr of an exec.ExitError, or err's own
// message when err carries none, for example when sudo itself is missing.
func exitStderr(err error) string {
	var ee *exec.ExitError
	if errors.As(err, &ee) && len(ee.Stderr) > 0 {
		return strings.TrimSpace(string(ee.Stderr))
	}
	return err.Error()
}

// runSudo runs the root copy under sudo -n with the given arguments. A nil
// stdin leaves stdin empty; a non-nil stdin feeds the bytes, for the plan.
func runSudo(ctx context.Context, stdin []byte, args ...string) error {
	full := append([]string{"-n", RootCopy}, args...)
	cmd := exec.CommandContext(ctx, "sudo", full...)
	// SIGINT instead of the default kill: sudo relays the signal to the root
	// copy, which stops the session unit before it exits. A kill of sudo
	// leaves the unit running, because sudo cannot relay a SIGKILL.
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = sudoWaitDelay
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
