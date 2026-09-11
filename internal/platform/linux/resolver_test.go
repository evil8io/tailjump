//go:build linux

package linux

import (
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// fakeRunner records every call and answers from a per-command-name queue,
// so a test can make resolvectl status fail while later calls succeed.
type fakeRunner struct {
	calls [][]string
	fail  map[string]error
}

func (f *fakeRunner) run(name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	return nil, f.fail[strings.Join(append([]string{name}, args...), " ")]
}

func newTestResolver(t *testing.T) (*Resolver, *fakeRunner) {
	t.Helper()
	dir := t.TempDir()
	fr := &fakeRunner{fail: map[string]error{}}
	r := &Resolver{
		run:            fr.run,
		resolvConfPath: filepath.Join(dir, "resolv.conf"),
		linkFile:       filepath.Join(dir, "run", "resolv.conf.link"),
		backupFile:     filepath.Join(dir, "run", "resolv.conf.backup"),
	}
	return r, fr
}

func addrs(t *testing.T, s ...string) []netip.Addr {
	t.Helper()
	out := make([]netip.Addr, len(s))
	for i, a := range s {
		p, err := netip.ParseAddr(a)
		if err != nil {
			t.Fatalf("parse addr %q: %v", a, err)
		}
		out[i] = p
	}
	return out
}

func TestAvailable(t *testing.T) {
	r, _ := newTestResolver(t)
	if !r.Available() {
		t.Fatal("want available when resolvectl status succeeds")
	}

	r2, fr := newTestResolver(t)
	fr.fail["resolvectl status"] = errors.New("no such service")
	if r2.Available() {
		t.Fatal("want not available when resolvectl status fails")
	}
}

func TestApplySplitBuildsResolvectlArgs(t *testing.T) {
	r, fr := newTestResolver(t)

	err := r.ApplySplit("tj0", addrs(t, "10.0.0.2"), []string{"corp.example", "eu-west-1.compute.internal"})
	if err != nil {
		t.Fatalf("ApplySplit: %v", err)
	}

	want := [][]string{
		{"resolvectl", "status"},
		{"resolvectl", "dns", "tj0", "10.0.0.2"},
		{"resolvectl", "domain", "tj0", "~corp.example", "~eu-west-1.compute.internal"},
		{"resolvectl", "default-route", "tj0", "false"},
	}
	if !reflect.DeepEqual(fr.calls, want) {
		t.Fatalf("got calls %v, want %v", fr.calls, want)
	}
}

func TestApplySplitNeedsDomains(t *testing.T) {
	r, _ := newTestResolver(t)
	err := r.ApplySplit("tj0", addrs(t, "10.0.0.2"), nil)
	if err == nil {
		t.Fatal("want an error with no domains")
	}
	if !strings.Contains(err.Error(), "dns.domains") {
		t.Fatalf("error %q must name the missing key dns.domains", err)
	}
}

func TestApplySplitNeedsResolved(t *testing.T) {
	r, fr := newTestResolver(t)
	fr.fail["resolvectl status"] = errors.New("no such service")

	err := r.ApplySplit("tj0", addrs(t, "10.0.0.2"), []string{"corp.example"})
	if err == nil {
		t.Fatal("want an error when resolved is not available")
	}
}

func TestApplyAllBuildsResolvectlArgs(t *testing.T) {
	r, fr := newTestResolver(t)

	err := r.ApplyAll("tj0", addrs(t, "10.0.0.2", "10.0.0.3"), []string{"eu-west-1.compute.internal"})
	if err != nil {
		t.Fatalf("ApplyAll: %v", err)
	}

	want := [][]string{
		{"resolvectl", "status"},
		{"resolvectl", "dns", "tj0", "10.0.0.2", "10.0.0.3"},
		{"resolvectl", "domain", "tj0", "~.", "eu-west-1.compute.internal"},
		{"resolvectl", "default-route", "tj0", "true"},
	}
	if !reflect.DeepEqual(fr.calls, want) {
		t.Fatalf("got calls %v, want %v", fr.calls, want)
	}
}

func TestRevertBuildsResolvectlArgs(t *testing.T) {
	r, fr := newTestResolver(t)
	if err := r.Revert("tj0"); err != nil {
		t.Fatalf("Revert: %v", err)
	}
	want := [][]string{
		{"resolvectl", "status"},
		{"resolvectl", "revert", "tj0"},
	}
	if !reflect.DeepEqual(fr.calls, want) {
		t.Fatalf("got calls %v, want %v", fr.calls, want)
	}
}

func TestRevertWithoutResolvedSkipsRevertCommand(t *testing.T) {
	r, fr := newTestResolver(t)
	fr.fail["resolvectl status"] = errors.New("no such service")
	if err := r.Revert("tj0"); err != nil {
		t.Fatalf("Revert: %v", err)
	}
	want := [][]string{{"resolvectl", "status"}}
	if !reflect.DeepEqual(fr.calls, want) {
		t.Fatalf("got calls %v, want %v", fr.calls, want)
	}
}

func TestApplyAllFallbackRegularFile(t *testing.T) {
	r, fr := newTestResolver(t)
	fr.fail["resolvectl status"] = errors.New("no such service")

	original := "nameserver 8.8.8.8\n"
	if err := os.WriteFile(r.resolvConfPath, []byte(original), 0o644); err != nil {
		t.Fatalf("seed resolv.conf: %v", err)
	}

	if err := r.ApplyAll("tj0", addrs(t, "10.0.0.2"), []string{"eu-west-1.compute.internal"}); err != nil {
		t.Fatalf("ApplyAll: %v", err)
	}

	got, err := os.ReadFile(r.resolvConfPath)
	if err != nil {
		t.Fatalf("read resolv.conf: %v", err)
	}
	if !strings.Contains(string(got), "nameserver 10.0.0.2\n") {
		t.Fatalf("resolv.conf %q must have the new nameserver", got)
	}
	if !strings.Contains(string(got), "search eu-west-1.compute.internal\n") {
		t.Fatalf("resolv.conf %q must have the search domain", got)
	}
	if fi, err := os.Lstat(r.resolvConfPath); err != nil || fi.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("resolv.conf must be a regular file after the fallback")
	}

	if err := r.Revert("tj0"); err != nil {
		t.Fatalf("Revert: %v", err)
	}
	restored, err := os.ReadFile(r.resolvConfPath)
	if err != nil {
		t.Fatalf("read reverted resolv.conf: %v", err)
	}
	if string(restored) != original {
		t.Fatalf("got restored %q, want %q", restored, original)
	}
	if _, err := os.Stat(r.backupFile); !os.IsNotExist(err) {
		t.Fatalf("backup file must be removed after revert, stat err %v", err)
	}

	// Revert is safe to call twice: nothing left to restore.
	if err := r.Revert("tj0"); err != nil {
		t.Fatalf("second Revert: %v", err)
	}
	restoredAgain, err := os.ReadFile(r.resolvConfPath)
	if err != nil {
		t.Fatalf("read resolv.conf after second revert: %v", err)
	}
	if string(restoredAgain) != original {
		t.Fatalf("a second revert must not change resolv.conf again, got %q", restoredAgain)
	}
}

func TestApplyAllFallbackSymlink(t *testing.T) {
	r, fr := newTestResolver(t)
	fr.fail["resolvectl status"] = errors.New("no such service")

	dir := filepath.Dir(r.resolvConfPath)
	target := filepath.Join(dir, "resolv.conf.systemd")
	if err := os.WriteFile(target, []byte("nameserver 100.100.100.100\n"), 0o644); err != nil {
		t.Fatalf("seed symlink target: %v", err)
	}
	if err := os.Symlink(target, r.resolvConfPath); err != nil {
		t.Fatalf("seed symlink: %v", err)
	}

	if err := r.ApplyAll("tj0", addrs(t, "10.0.0.2"), nil); err != nil {
		t.Fatalf("ApplyAll: %v", err)
	}
	if fi, err := os.Lstat(r.resolvConfPath); err != nil || fi.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("resolv.conf must be a regular file after the fallback, not a symlink")
	}

	if err := r.Revert("tj0"); err != nil {
		t.Fatalf("Revert: %v", err)
	}
	fi, err := os.Lstat(r.resolvConfPath)
	if err != nil {
		t.Fatalf("lstat reverted resolv.conf: %v", err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("resolv.conf must be a symlink again after revert")
	}
	got, err := os.Readlink(r.resolvConfPath)
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if got != target {
		t.Fatalf("got symlink target %q, want %q", got, target)
	}
	if _, err := os.Stat(r.linkFile); !os.IsNotExist(err) {
		t.Fatalf("link file must be removed after revert, stat err %v", err)
	}
}

func TestRevertWithNoFallbackAppliedIsNoop(t *testing.T) {
	r, fr := newTestResolver(t)
	fr.fail["resolvectl status"] = errors.New("no such service")

	if err := r.Revert("tj0"); err != nil {
		t.Fatalf("Revert: %v", err)
	}
	if _, err := os.Stat(r.resolvConfPath); !os.IsNotExist(err) {
		t.Fatalf("revert must not create resolv.conf when nothing was applied")
	}
}
