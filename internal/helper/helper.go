// Package helper is the remote side of tailjump: the tj-helper binary and the
// tj _remote subcommand. It imports no package that imports tailscale.com,
// cobra, or gvisor, so the helper stays small.
package helper

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/evil8io/tailjump/internal/mux"
	"github.com/evil8io/tailjump/internal/version"
)

// filePrefix is the name the upload command gives the helper file.
const filePrefix = "tj-helper."

// staleAge is the age at which a starting helper removes another helper file
// from its own directory. The client removes the file through the unlink verb
// and every helper removes it at exit, so a file this old is the residue of a
// SIGKILL or a power loss.
const staleAge = 10 * time.Minute

// Main runs the remote helper over stdin and stdout and exits the process. It
// is the entry point for cmd/tjhelper and for tj _remote.
func Main() {
	if err := Run(os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "tj-helper:", err)
		os.Exit(1)
	}
}

// Run serves the mux over the given transport. The reader and the writer are
// the mux transport, and logw is the helper log. It ignores SIGPIPE, because
// the Go runtime otherwise ends the process on a write to a closed stdout
// without the deferred remove of the helper file; with the signal ignored the
// write fails, the mux closes, and the exit path runs.
func Run(stdin io.Reader, stdout io.Writer, logw io.Writer) error {
	signal.Ignore(syscall.SIGPIPE)
	var self string
	if len(os.Args) > 0 {
		self = os.Args[0]
	}
	return run(self, stdin, stdout, logw)
}

// run serves the mux and owns the lifetime of the helper file. self is that
// file. The client starts one helper per lane from it, so the helper keeps it
// until the client sends unlink, and removes it at exit for the paths that
// never get there. At start it also removes the stale helper files next to it.
// It removes the cache directory at exit when the directory is empty.
func run(self string, stdin io.Reader, stdout io.Writer, logw io.Writer) error {
	sweepStale(self, time.Now())
	defer cleanupCache()
	defer func() { _ = removeSelf(self) }()

	hostname, _ := os.Hostname()
	srv := &mux.Server{
		Info: mux.ControlInfo{
			Version:  version.Version,
			GOOS:     runtime.GOOS,
			GOARCH:   runtime.GOARCH,
			Hostname: hostname,
			PID:      os.Getpid(),
		},
		LogW:   logw,
		Unlink: func() error { return removeSelf(self) },
	}
	return srv.Serve(transport{r: stdin, w: stdout})
}

// ArchForUname maps the output of uname -m to a Go architecture. The client
// uses it to pick the helper binary to upload.
func ArchForUname(unameM string) (string, error) {
	switch unameM {
	case "x86_64":
		return "amd64", nil
	case "aarch64":
		return "arm64", nil
	default:
		return "", fmt.Errorf("unsupported remote architecture %q", unameM)
	}
}

// removeSelf removes the helper's own file. The unlink verb and the exit both
// call it, and the second call finds the file gone, which is not an error.
func removeSelf(self string) error {
	if self == "" {
		return nil
	}
	if err := os.Remove(self); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// sweepStale removes every helper file in self's directory that is older than
// staleAge. It never removes self, and it never removes a file of a helper
// that another client started in the last staleAge.
func sweepStale(self string, now time.Time) {
	if self == "" {
		return
	}
	dir := filepath.Dir(self)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	own := filepath.Base(self)
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || name == own || !strings.HasPrefix(name, filePrefix) {
			continue
		}
		info, err := e.Info()
		if err != nil || now.Sub(info.ModTime()) < staleAge {
			continue
		}
		_ = os.Remove(filepath.Join(dir, name))
	}
}

func cleanupCache() {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	// os.Remove removes the directory only when it is empty.
	_ = os.Remove(filepath.Join(home, ".cache", "tj"))
}

// transport joins a reader and a writer into the io.ReadWriteCloser that yamux
// needs. Close is a no-op because the process exit closes the file descriptors.
type transport struct {
	r io.Reader
	w io.Writer
}

func (t transport) Read(p []byte) (int, error)  { return t.r.Read(p) }
func (t transport) Write(p []byte) (int, error) { return t.w.Write(p) }
func (transport) Close() error                  { return nil }
