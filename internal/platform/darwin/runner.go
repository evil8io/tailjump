//go:build darwin

package darwin

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const stopTimeout = 20 * time.Second

// Runner implements platform.Runner for macOS with a detached child
// process and a PID file, in place of a systemd unit.
type Runner struct{}

func NewRunner() *Runner { return &Runner{} }

func pidFilePath() string {
	return filepath.Join(runtimeDir, "runner.pid")
}

func logFilePath() string {
	return filepath.Join(runtimeDir, "session.log")
}

// Start re-executes the running binary as "<self> _session run <plan>",
// detached from the current session, and records its PID so a later,
// separate process can find it again through Stop or Active.
func (r *Runner) Start(plan string) error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("find the running executable: %w", err)
	}
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", runtimeDir, err)
	}
	logFile, err := os.OpenFile(logFilePath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		return fmt.Errorf("open %s: %w", logFilePath(), err)
	}
	defer func() { _ = logFile.Close() }()

	cmd := exec.Command(self, "_session", "run", plan)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start the session process: %w", err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Process.Release(); err != nil {
		return fmt.Errorf("detach the session process: %w", err)
	}
	if err := os.WriteFile(pidFilePath(), []byte(strconv.Itoa(pid)+"\n"), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", pidFilePath(), err)
	}
	return nil
}

// Stop sends SIGTERM to the recorded process and waits for it to exit, the
// way "systemctl stop" waits on Linux. It is a no-op when no PID file
// exists, so a repeat call is safe.
func (r *Runner) Stop() error {
	pid, ok, err := readPID()
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	proc, _ := os.FindProcess(pid)
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		if isProcessGone(err) {
			return clearPIDFile()
		}
		return fmt.Errorf("signal process %d: %w", pid, err)
	}
	deadline := time.Now().Add(stopTimeout)
	for time.Now().Before(deadline) {
		active, err := r.Active()
		if err != nil {
			return err
		}
		if !active {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("session process %d did not stop within %s", pid, stopTimeout)
}

// Active reports whether the recorded process is still alive. It clears a
// stale PID file when the process is gone.
func (r *Runner) Active() (bool, error) {
	pid, ok, err := readPID()
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	proc, _ := os.FindProcess(pid)
	err = proc.Signal(syscall.Signal(0))
	if err == nil {
		return true, nil
	}
	if isProcessGone(err) {
		if err := clearPIDFile(); err != nil {
			return false, err
		}
		return false, nil
	}
	// The process exists but belongs to another user, for example root.
	return true, nil
}

func isProcessGone(err error) bool {
	return errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH)
}

func clearPIDFile() error {
	if err := os.Remove(pidFilePath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", pidFilePath(), err)
	}
	return nil
}

func readPID() (int, bool, error) {
	data, err := os.ReadFile(pidFilePath())
	if errors.Is(err, os.ErrNotExist) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("read %s: %w", pidFilePath(), err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, false, fmt.Errorf("parse %s: %w", pidFilePath(), err)
	}
	return pid, true, nil
}
