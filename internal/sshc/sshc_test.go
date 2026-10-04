package sshc

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"net/netip"
	"os"
	"os/user"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
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

// stallClient returns a Client on an SSH server that accepts every exec
// request and then neither reads stdin nor exits, so a command on it never
// completes. The server is in-process, on the loopback.
func stallClient(t *testing.T) *Client {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate host key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatalf("host key signer: %v", err)
	}
	cfg := &ssh.ServerConfig{NoClientAuth: true}
	cfg.AddHostKey(signer)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			go serveStall(nc, cfg)
		}
	}()

	conn, err := ssh.Dial("tcp", ln.Addr().String(), &ssh.ClientConfig{
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         DialTimeout,
	})
	if err != nil {
		t.Fatalf("dial the stall server: %v", err)
	}
	c := &Client{conn: conn, hostname: "stall", closeCh: make(chan struct{})}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func serveStall(nc net.Conn, cfg *ssh.ServerConfig) {
	conn, chans, reqs, err := ssh.NewServerConn(nc, cfg)
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()
	go ssh.DiscardRequests(reqs)
	for newCh := range chans {
		_, chReqs, err := newCh.Accept()
		if err != nil {
			continue
		}
		go func() {
			for req := range chReqs {
				if req.WantReply {
					_ = req.Reply(req.Type == "exec", nil)
				}
			}
		}()
	}
}

func TestRunContextDeadlineClosesTheConnection(t *testing.T) {
	c := stallClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	// 4 MiB exceeds the 2 MiB channel window, so the stdin copy blocks until
	// the server reads, which it never does.
	start := time.Now()
	_, err := c.RunContext(ctx, "cat", make([]byte, 4<<20))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want a deadline error, got %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("RunContext returned after %s, want it within the limit", d)
	}

	closed := make(chan error, 1)
	go func() { closed <- c.conn.Wait() }()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("the connection is still open after the deadline")
	}
}
