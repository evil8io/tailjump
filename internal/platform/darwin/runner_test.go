//go:build darwin

package darwin

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
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

func TestLogsNoFile(t *testing.T) {
	withTempRuntimeDir(t)
	r := &Runner{}
	var buf bytes.Buffer
	if err := r.Logs(context.Background(), &buf, 10, time.Time{}, false); err != nil {
		t.Fatalf("Logs with no file: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("Logs with no file wrote %q, want empty", buf.String())
	}
}

func TestLogsLines(t *testing.T) {
	withTempRuntimeDir(t)
	content := "line1\nline2\nline3\n"
	if err := os.WriteFile(logFilePath(), []byte(content), 0o640); err != nil {
		t.Fatalf("write log file: %v", err)
	}
	r := &Runner{}

	var all bytes.Buffer
	if err := r.Logs(context.Background(), &all, 0, time.Time{}, false); err != nil {
		t.Fatalf("Logs lines=0: %v", err)
	}
	if all.String() != content {
		t.Errorf("Logs lines=0 = %q, want %q", all.String(), content)
	}

	var last bytes.Buffer
	if err := r.Logs(context.Background(), &last, 2, time.Time{}, false); err != nil {
		t.Fatalf("Logs lines=2: %v", err)
	}
	if want := "line2\nline3\n"; last.String() != want {
		t.Errorf("Logs lines=2 = %q, want %q", last.String(), want)
	}
}
