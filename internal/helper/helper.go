// Package helper is the remote side of tailjump: the tj-helper binary and the
// tj _remote subcommand. It imports no package that imports tailscale.com,
// cobra, or gvisor, so the helper stays small.
package helper

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/evil8io/tailjump/internal/mux"
	"github.com/evil8io/tailjump/internal/version"
)

// Main runs the remote helper over stdin and stdout and exits the process. It
// is the entry point for cmd/tjhelper and for tj _remote.
func Main() {
	if err := Run(os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "tj-helper:", err)
		os.Exit(1)
	}
}

// Run serves the mux over the given transport. It deletes its own binary right
// after start, so no file remains after a crash, and it removes the cache
// directory at exit when the directory is empty. The reader and the writer are
// the mux transport, and logw is the helper log.
func Run(stdin io.Reader, stdout io.Writer, logw io.Writer) error {
	selfDelete()
	defer cleanupCache()

	hostname, _ := os.Hostname()
	srv := &mux.Server{
		Info: mux.ControlInfo{
			Version:  version.Version,
			GOOS:     runtime.GOOS,
			GOARCH:   runtime.GOARCH,
			Hostname: hostname,
			PID:      os.Getpid(),
		},
		LogW: logw,
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

func selfDelete() {
	if len(os.Args) > 0 && os.Args[0] != "" {
		_ = os.Remove(os.Args[0])
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
