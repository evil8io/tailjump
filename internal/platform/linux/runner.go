//go:build linux

package linux

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// unitName is the transient systemd unit that runs one session.
const unitName = "tj-session"

// Runner implements platform.Runner for Linux. It runs the session as a
// transient systemd unit through systemd-run, so a logout or a crash of the
// starting shell does not end the session.
type Runner struct{}

func NewRunner() *Runner { return &Runner{} }

// Start runs the session runner in a transient unit. systemd-run reaps the
// unit with --collect, kills the control group on stop, and runs the cleanup
// command through ExecStopPost so a crash still reverts the session.
func (r *Runner) Start(plan string) error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("find own path: %w", err)
	}
	args := []string{
		"--unit", unitName,
		"--collect",
		"--property", "KillMode=mixed",
		"--property", "TimeoutStopSec=20",
		"--property", fmt.Sprintf("ExecStopPost=%s _session cleanup", self),
		self, "_session", "run", plan,
	}
	cmd := exec.Command("systemd-run", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemd-run: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Stop stops the transient unit. A missing unit is not an error.
func (r *Runner) Stop() error {
	cmd := exec.Command("systemctl", "stop", unitName)
	out, err := cmd.CombinedOutput()
	if err != nil {
		text := strings.TrimSpace(string(out))
		if strings.Contains(text, "not loaded") || strings.Contains(text, "not found") {
			return nil
		}
		return fmt.Errorf("systemctl stop: %w: %s", err, text)
	}
	return nil
}

// Active reports whether the transient unit is active. systemctl is-active
// exits 0 for an active unit and non-zero for an inactive or absent one, so a
// clean exit is the only positive signal.
func (r *Runner) Active() (bool, error) {
	err := exec.Command("systemctl", "is-active", "--quiet", unitName).Run()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return false, nil
	}
	return false, fmt.Errorf("systemctl is-active: %w", err)
}

// Logs writes the tj-session journal to w through journalctl, stopping the
// process when ctx ends. lines 0 means every line; since zero means no
// --since bound.
func (r *Runner) Logs(ctx context.Context, w io.Writer, lines int, since time.Time, follow bool) error {
	n := "all"
	if lines > 0 {
		n = strconv.Itoa(lines)
	}
	args := []string{"-u", unitName, "--no-pager", "-o", "short-iso", "-n", n}
	if !since.IsZero() {
		args = append(args, "--since", fmt.Sprintf("@%d", since.Unix()))
	}
	if follow {
		args = append(args, "-f")
	}

	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "journalctl", args...)
	cmd.Stdout = w
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil && ctx.Err() == nil {
		return fmt.Errorf("journalctl: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}
