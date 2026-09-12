package helper

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/evil8io/tailjump/internal/mux"
)

func TestArchForUname(t *testing.T) {
	cases := map[string]string{
		"x86_64":  "amd64",
		"aarch64": "arm64",
	}
	for unameM, want := range cases {
		got, err := ArchForUname(unameM)
		if err != nil {
			t.Fatalf("ArchForUname(%q): %v", unameM, err)
		}
		if got != want {
			t.Fatalf("ArchForUname(%q) = %q, want %q", unameM, got, want)
		}
	}
}

func TestArchForUnameUnsupported(t *testing.T) {
	_, err := ArchForUname("armv7l")
	if err == nil {
		t.Fatal("ArchForUname(\"armv7l\") returned no error")
	}
	if !strings.Contains(err.Error(), "unsupported remote architecture") {
		t.Fatalf("error = %q, want it to name the unsupported architecture", err.Error())
	}
}

// TestSweepStale checks the start sweep: it removes a helper file older than
// staleAge, keeps a recent one, keeps its own file whatever its age, and
// leaves another name alone.
func TestSweepStale(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	stale := now.Add(-11 * time.Minute)

	self := writeFile(t, dir, "tj-helper.100", stale)
	old := writeFile(t, dir, "tj-helper.42", stale)
	recent := writeFile(t, dir, "tj-helper.43", now.Add(-1*time.Minute))
	other := writeFile(t, dir, "known_hosts", stale)

	sweepStale(self, now)

	if _, err := os.Stat(old); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stat %s = %v, want the stale helper file removed", old, err)
	}
	for _, keep := range []string{self, recent, other} {
		if _, err := os.Stat(keep); err != nil {
			t.Fatalf("stat %s: %v, want the file kept", keep, err)
		}
	}
}

// TestUnlinkAndExitRemoveTheHelperFile checks the file lifetime: the helper
// keeps its file until the client sends unlink, and a helper that never gets
// the verb removes the file at its exit.
func TestUnlinkAndExitRemoveTheHelperFile(t *testing.T) {
	t.Run("unlink", func(t *testing.T) {
		self, client, done := startHelper(t)
		if err := client.Unlink(); err != nil {
			t.Fatalf("Unlink: %v", err)
		}
		waitGone(t, self)
		stopHelper(t, client)
		waitDone(t, done)
	})
	t.Run("exit", func(t *testing.T) {
		self, client, done := startHelper(t)
		if _, err := os.Stat(self); err != nil {
			t.Fatalf("stat %s: %v, want the file kept while the helper runs", self, err)
		}
		stopHelper(t, client)
		waitDone(t, done)
		waitGone(t, self)
	})
}

// startHelper runs a helper over net.Pipe with its own file in a temp
// directory, and returns that file, the mux client, and the run result.
func startHelper(t *testing.T) (string, *mux.Client, <-chan error) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	self := writeFile(t, t.TempDir(), "tj-helper.777", time.Now())

	c1, c2 := net.Pipe()
	done := make(chan error, 1)
	go func() { done <- run(self, c2, c2, nil) }()
	client, err := mux.NewClient(c1)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() {
		_ = client.Close()
		_ = c2.Close()
	})
	return self, client, done
}

// stopHelper ends the helper as the session stop path does: quit on the
// control stream, then a close of the mux, which ends the helper's transport.
func stopHelper(t *testing.T, client *mux.Client) {
	t.Helper()
	if err := client.Quit(); err != nil {
		t.Fatalf("Quit: %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func writeFile(t *testing.T, dir, name string, mtime time.Time) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("helper"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	return path
}

func waitGone(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s is still there after a second, want it removed", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitDone(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the helper did not return after quit")
	}
}
