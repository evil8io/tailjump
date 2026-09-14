package session

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
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

// sudoInterruptCode is the exit status of a root copy that a signal stopped.
const sudoInterruptCode = 130

// ErrInterrupted reports that a signal stopped the root copy. sudo 1.9.14 and
// later run it in a pseudo-terminal, so Ctrl-C reaches that child alone and
// this process gets no signal of its own.
var ErrInterrupted = errors.New("interrupted")

// Connect starts the session. A session to the plan's address is already the
// session the caller asks for, so Connect reports it and returns nil; a
// session to another address is refused without replace. When the effective
// uid is 0 it runs the start in-process; otherwise it re-execs through sudo to
// the root copy. start is the time the command began, for the final line.
func Connect(ctx context.Context, out, errw io.Writer, plan *Plan, replace, foreground bool, start time.Time) error {
	plat := platform.New()

	active, ae, err := activeSession(plat)
	if err != nil {
		return fmt.Errorf("check active session: %w", err)
	}
	if active {
		if !replace {
			st := alreadyUp(plat, plan)
			if st == nil {
				return ae
			}
			_, err := fmt.Fprintf(out, "session to %s already up (%s); use --replace to restart\n", st.Remote, st.Uptime())
			return err
		}
		if err := stopActive(ctx, out, errw, plat); err != nil {
			return fmt.Errorf("replace active session: %w", err)
		}
	}

	planJSON, err := plan.Marshal()
	if err != nil {
		return err
	}

	if os.Geteuid() == 0 {
		err = Start(ctx, errw, planJSON, foreground)
	} else {
		err = sudoStart(ctx, out, errw, planJSON, foreground, plan.Verbose)
	}
	if err != nil {
		return err
	}
	printUp(out, start)
	return nil
}

// alreadyUp returns the state of the active session when its address is the
// address of the plan. A missing state file returns nil, because the session
// the unit runs is then unknown.
func alreadyUp(plat platform.Platform, plan *Plan) *State {
	st, err := ReadState(StatePath(plat.Paths.RuntimeDir()))
	if err != nil || st.Addr != plan.Addr {
		return nil
	}
	return st
}

// printUp prints the final line of a connect from the state the session
// wrote. A state that is gone or unreadable prints no line, because the
// connect itself succeeded.
func printUp(w io.Writer, start time.Time) {
	st, err := Active()
	if err != nil {
		slog.Warn("read session state", "error", err)
		return
	}
	if st == nil {
		return
	}
	_, _ = fmt.Fprintf(w, "session to %s up: %s, dns %s, %d networks, %s\n",
		st.Remote, st.TransportLine(), st.DNS.Mode, len(st.Networks), time.Since(start).Round(100*time.Millisecond))
}

// stopActive ends the active session and waits until the unit is inactive, so
// a following start does not clash with the old unit name.
func stopActive(ctx context.Context, out, errw io.Writer, plat platform.Platform) error {
	if os.Geteuid() == 0 {
		if err := Stop(); err != nil {
			return err
		}
	} else {
		if err := runSudo(ctx, out, errw, nil, "_session", "stop"); err != nil {
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
// with a clear message instead of a subtle mismatch. verbose puts -v on the
// root copy, so its progress stream and its own log reach the terminal.
func sudoStart(ctx context.Context, out, errw io.Writer, planJSON []byte, foreground, verbose bool) error {
	if err := CheckRootCopy(ctx); err != nil {
		return err
	}
	var args []string
	if verbose {
		args = append(args, "-v")
	}
	args = append(args, "_session", "start")
	if foreground {
		args = append(args, "--foreground")
	}
	return runSudo(ctx, out, errw, planJSON, args...)
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
func runSudo(ctx context.Context, out, errw io.Writer, stdin []byte, args ...string) error {
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
	cmd.Stdout = out
	cmd.Stderr = errw
	err := cmd.Run()
	// sudo runs the root copy in a pseudo-terminal, so a Ctrl-C reaches that
	// child through the pty and this process gets no signal. The exit status
	// of the child is then the only report of the interrupt.
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == sudoInterruptCode {
		return ErrInterrupted
	}
	return err
}
