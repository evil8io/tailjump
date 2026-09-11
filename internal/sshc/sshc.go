// Package sshc is the tj SSH client: dial, exec, banner, host key store, and
// keepalive. It authenticates against Tailscale SSH, which accepts the
// "none" method because the tailnet already authenticated the peer.
package sshc

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

const (
	// DialTimeout is the TCP and handshake timeout for a new connection.
	DialTimeout = 15 * time.Second
	// keepaliveInterval is the period of the liveness keepalive.
	keepaliveInterval = 30 * time.Second
)

// Client is an SSH connection to one remote.
type Client struct {
	conn     *ssh.Client
	hostname string

	closeOnce sync.Once
	closeCh   chan struct{}
}

// Dial opens an SSH connection to addr on port 22, as user. hostname names
// the remote for the host key store and for log messages; cacheDir is
// where known host keys persist, keyed by hostname.
//
// It authenticates with the "none" method first, which Tailscale SSH
// accepts because WireGuard already authenticated the node. When the
// server refuses "none", it retries with ssh-agent keys from
// $SSH_AUTH_SOCK and a keyboard-interactive handler, because a check-mode
// server can challenge on either.
func Dial(ctx context.Context, addr netip.Addr, hostname, user, cacheDir string) (*Client, error) {
	target := net.JoinHostPort(addr.String(), "22")
	kh := newKnownHosts(cacheDir)

	config := func(auth []ssh.AuthMethod) *ssh.ClientConfig {
		return &ssh.ClientConfig{
			User:            user,
			Auth:            auth,
			HostKeyCallback: kh.callback(hostname),
			BannerCallback: func(message string) error {
				if message != "" {
					fmt.Fprint(os.Stderr, message)
				}
				return nil
			},
			Timeout: DialTimeout,
		}
	}

	conn, err := dial(ctx, target, config(nil))
	if err != nil {
		extra, closer := fallbackAuthMethods()
		if closer != nil {
			defer func() { _ = closer.Close() }()
		}
		conn, err = dial(ctx, target, config(extra))
		if err != nil {
			return nil, fmt.Errorf("ssh dial %s: %w", target, err)
		}
	}

	c := &Client{conn: conn, hostname: hostname, closeCh: make(chan struct{})}
	go c.keepaliveLoop()
	return c, nil
}

func dial(ctx context.Context, addr string, config *ssh.ClientConfig) (*ssh.Client, error) {
	d := net.Dialer{Timeout: DialTimeout}
	nc, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	conn, chans, reqs, err := ssh.NewClientConn(nc, addr, config)
	if err != nil {
		return nil, err
	}
	return ssh.NewClient(conn, chans, reqs), nil
}

func fallbackAuthMethods() ([]ssh.AuthMethod, io.Closer) {
	var methods []ssh.AuthMethod
	var closer io.Closer
	if am, c, err := agentAuthMethod(); err == nil {
		methods = append(methods, am)
		closer = c
	} else {
		slog.Debug("ssh-agent unavailable", "error", err)
	}
	methods = append(methods, keyboardInteractiveMethod())
	return methods, closer
}

// keepaliveLoop sends keepalive@openssh.com every 30s until Close. The
// server answers with ok=false and a nil error; that completed round trip
// is the liveness signal, not the reply value.
func (c *Client) keepaliveLoop() {
	ticker := time.NewTicker(keepaliveInterval)
	defer ticker.Stop()
	for {
		select {
		case <-c.closeCh:
			return
		case <-ticker.C:
			if _, _, err := c.conn.SendRequest("keepalive@openssh.com", true, nil); err != nil {
				slog.Debug("ssh keepalive failed", "hostname", c.hostname, "error", err)
			}
		}
	}
}

// Close stops the keepalive loop and closes the connection.
func (c *Client) Close() error {
	c.closeOnce.Do(func() { close(c.closeCh) })
	return c.conn.Close()
}

// Run executes cmd (run as /bin/bash -c by Tailscale SSH), feeds stdin, and
// collects stdout. A non-zero exit returns an error that wraps
// *ssh.ExitError.
func (c *Client) Run(cmd string, stdin []byte) ([]byte, error) {
	session, err := c.conn.NewSession()
	if err != nil {
		return nil, fmt.Errorf("new ssh session: %w", err)
	}
	defer func() { _ = session.Close() }()

	var stdout, stderr strings.Builder
	session.Stdout = &stdout
	session.Stderr = &stderr
	if stdin != nil {
		session.Stdin = bytes.NewReader(stdin)
	}

	if err := session.Run(cmd); err != nil {
		return []byte(stdout.String()), fmt.Errorf("run %q: %w (stderr: %s)", cmd, err, strings.TrimSpace(stderr.String()))
	}
	return []byte(stdout.String()), nil
}

// Exec starts cmd in a new session and returns a ReadWriteCloser over its
// stdin and stdout. This is the mux transport for a later chunk; chunk 2
// does not use it. Close ends the session.
func (c *Client) Exec(cmd string) (io.ReadWriteCloser, error) {
	session, err := c.conn.NewSession()
	if err != nil {
		return nil, fmt.Errorf("new ssh session: %w", err)
	}
	stdin, err := session.StdinPipe()
	if err != nil {
		_ = session.Close()
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		_ = session.Close()
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	session.Stderr = os.Stderr
	if err := session.Start(cmd); err != nil {
		_ = session.Close()
		return nil, fmt.Errorf("start %q: %w", cmd, err)
	}
	return &execChannel{session: session, stdin: stdin, stdout: stdout}, nil
}

type execChannel struct {
	session *ssh.Session
	stdin   io.WriteCloser
	stdout  io.Reader
}

func (e *execChannel) Read(p []byte) (int, error)  { return e.stdout.Read(p) }
func (e *execChannel) Write(p []byte) (int, error) { return e.stdin.Write(p) }

func (e *execChannel) Close() error {
	_ = e.stdin.Close()
	return e.session.Close()
}
