//go:build darwin

package darwin

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
)

func TestPidAndLogFilePaths(t *testing.T) {
	withTempRuntimeDir(t)
	if got, want := pidFilePath(), filepath.Join(runtimeDir, "runner.pid"); got != want {
		t.Errorf("pidFilePath() = %q, want %q", got, want)
	}
	if got, want := logFilePath(), filepath.Join(runtimeDir, "session.log"); got != want {
		t.Errorf("logFilePath() = %q, want %q", got, want)
	}
}

func TestReadPID(t *testing.T) {
	withTempRuntimeDir(t)

	if _, ok, err := readPID(); err != nil || ok {
		t.Fatalf("readPID with no file: ok=%v err=%v, want ok=false err=nil", ok, err)
	}

	if err := os.WriteFile(pidFilePath(), []byte("4242\n"), 0o644); err != nil {
		t.Fatalf("write pid file: %v", err)
	}
	pid, ok, err := readPID()
	if err != nil {
		t.Fatalf("readPID: %v", err)
	}
	if !ok || pid != 4242 {
		t.Errorf("readPID() = %d, %v, want 4242, true", pid, ok)
	}

	if err := os.WriteFile(pidFilePath(), []byte("not-a-pid"), 0o644); err != nil {
		t.Fatalf("write pid file: %v", err)
	}
	if _, _, err := readPID(); err == nil {
		t.Error("readPID with malformed content should error")
	}
}

func TestActiveNoPIDFile(t *testing.T) {
	withTempRuntimeDir(t)
	r := &Runner{}
	active, err := r.Active()
	if err != nil {
		t.Fatalf("Active: %v", err)
	}
	if active {
		t.Error("Active() = true with no PID file, want false")
	}
	if err := r.Stop(); err != nil {
		t.Fatalf("Stop with no PID file: %v", err)
	}
}

func TestActiveStalePID(t *testing.T) {
	withTempRuntimeDir(t)
	// A pid past macOS's default pid_max, so it can never be in use.
	if err := os.WriteFile(pidFilePath(), []byte("2147483647\n"), 0o644); err != nil {
		t.Fatalf("write pid file: %v", err)
	}
	r := &Runner{}
	active, err := r.Active()
	if err != nil {
		t.Fatalf("Active: %v", err)
	}
	if active {
		t.Error("Active() = true for a stale pid, want false")
	}
	if _, err := os.Stat(pidFilePath()); !os.IsNotExist(err) {
		t.Error("stale pid file was not cleared")
	}
}

// TestActiveAndStop uses a real "sleep" child in place of a session
// process, since Active and Stop only need a live pid to signal.
func TestActiveAndStop(t *testing.T) {
	withTempRuntimeDir(t)

	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Wait() })

	if err := os.WriteFile(pidFilePath(), []byte(strconv.Itoa(cmd.Process.Pid)+"\n"), 0o644); err != nil {
		t.Fatalf("write pid file: %v", err)
	}

	r := &Runner{}
	active, err := r.Active()
	if err != nil {
		t.Fatalf("Active: %v", err)
	}
	if !active {
		t.Fatal("Active() = false, want true")
	}

	if err := r.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	active, err = r.Active()
	if err != nil {
		t.Fatalf("Active after Stop: %v", err)
	}
	if active {
		t.Error("Active() = true after Stop, want false")
	}
	if _, err := os.Stat(pidFilePath()); !os.IsNotExist(err) {
		t.Error("pid file still exists after Stop")
	}
}
