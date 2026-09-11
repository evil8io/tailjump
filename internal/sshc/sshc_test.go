package sshc

import (
	"context"
	"net/netip"
	"os"
	"os/user"
	"testing"
	"time"
)

// testRemote reads the real gateway address from TJ_TEST_REMOTE and skips
// the test when it is unset, so CI without a gateway passes. See
// CLAUDE.md, "Public repository hygiene".
func testRemote(t *testing.T) netip.Addr {
	t.Helper()
	s := os.Getenv("TJ_TEST_REMOTE")
	if s == "" {
		t.Skip("TJ_TEST_REMOTE not set, skipping a test that dials a real gateway")
	}
	addr, err := netip.ParseAddr(s)
	if err != nil {
		t.Fatalf("TJ_TEST_REMOTE %q: %v", s, err)
	}
	return addr
}

func testUser() string {
	if u := os.Getenv("TJ_TEST_USER"); u != "" {
		return u
	}
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return "root"
}

func TestDialAndRunAgainstRealGateway(t *testing.T) {
	addr := testRemote(t)
	ctx, cancel := context.WithTimeout(context.Background(), DialTimeout)
	defer cancel()

	c, err := Dial(ctx, addr, "tj-sshc-test", testUser(), t.TempDir())
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = c.Close() }()

	out, err := c.Run("uname -m", nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("want non-empty uname -m output")
	}
}

func TestRunFeedsStdin(t *testing.T) {
	addr := testRemote(t)
	ctx, cancel := context.WithTimeout(context.Background(), DialTimeout)
	defer cancel()

	c, err := Dial(ctx, addr, "tj-sshc-test", testUser(), t.TempDir())
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = c.Close() }()

	out, err := c.Run("cat", []byte("hello from tj\n"))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if string(out) != "hello from tj\n" {
		t.Fatalf("want stdin echoed back, got %q", out)
	}
}

func TestRunNonZeroExitIsExitError(t *testing.T) {
	addr := testRemote(t)
	ctx, cancel := context.WithTimeout(context.Background(), DialTimeout)
	defer cancel()

	c, err := Dial(ctx, addr, "tj-sshc-test", testUser(), t.TempDir())
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = c.Close() }()

	if _, err := c.Run("exit 3", nil); err == nil {
		t.Fatal("want an error for a non-zero exit")
	}
}

func TestExecRoundTrip(t *testing.T) {
	addr := testRemote(t)
	ctx, cancel := context.WithTimeout(context.Background(), DialTimeout)
	defer cancel()

	c, err := Dial(ctx, addr, "tj-sshc-test", testUser(), t.TempDir())
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = c.Close() }()

	ch, err := c.Exec("cat")
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	defer func() { _ = ch.Close() }()

	if _, err := ch.Write([]byte("ping\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	buf := make([]byte, 5)
	done := make(chan struct{})
	var n int
	var rerr error
	go func() {
		n, rerr = ch.Read(buf)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(DialTimeout):
		t.Fatal("timed out reading the exec channel")
	}
	if rerr != nil {
		t.Fatalf("Read: %v", rerr)
	}
	if string(buf[:n]) != "ping\n" {
		t.Fatalf("want ping echoed back, got %q", buf[:n])
	}
}
