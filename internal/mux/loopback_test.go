package mux

import (
	"bytes"
	"errors"
	"io"
	"net"
	"net/netip"
	"testing"
	"time"
)

// newLoopback wires a mux client to a mux server over net.Pipe, in one
// process, with no device and no root.
func newLoopback(t *testing.T, srv *Server) *Client {
	t.Helper()
	c1, c2 := net.Pipe()
	go func() { _ = srv.Serve(c2) }()
	client, err := NewClient(c1)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() {
		_ = client.Close()
		_ = c2.Close()
	})
	return client
}

func tcpEchoListener(t *testing.T) netip.AddrPort {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	return ln.Addr().(*net.TCPAddr).AddrPort()
}

func udpEchoServer(t *testing.T) netip.AddrPort {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	go func() {
		buf := make([]byte, 65535)
		for {
			n, addr, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			if _, err := conn.WriteToUDP(buf[:n], addr); err != nil {
				return
			}
		}
	}()
	return conn.LocalAddr().(*net.UDPAddr).AddrPort()
}

func TestLoopbackTCP(t *testing.T) {
	dst := tcpEchoListener(t)
	client := newLoopback(t, &Server{})

	conn, err := client.DialTCP(dst)
	if err != nil {
		t.Fatalf("DialTCP: %v", err)
	}
	defer func() { _ = conn.Close() }()

	want := []byte("hello over the mux")
	if _, err := conn.Write(want); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := make([]byte, len(want))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("echo = %q, want %q", got, want)
	}
}

func TestLoopbackTCPRefused(t *testing.T) {
	// Bind and close a listener to get a port that refuses.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dst := ln.Addr().(*net.TCPAddr).AddrPort()
	_ = ln.Close()

	client := newLoopback(t, &Server{})
	if _, err := client.DialTCP(dst); !errors.Is(err, ErrRefused) {
		t.Fatalf("DialTCP to a closed port = %v, want ErrRefused", err)
	}
}

func TestLoopbackUDP(t *testing.T) {
	dst := udpEchoServer(t)
	client := newLoopback(t, &Server{})

	conn, err := client.DialUDP(dst)
	if err != nil {
		t.Fatalf("DialUDP: %v", err)
	}
	defer func() { _ = conn.Close() }()

	want := []byte("ping")
	if err := conn.WriteFrame(want); err != nil {
		t.Fatalf("WriteFrame: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	got, err := conn.ReadFrame()
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("echo = %q, want %q", got, want)
	}
}

func TestLoopbackUDPIdleClose(t *testing.T) {
	dst := udpEchoServer(t)
	client := newLoopback(t, &Server{UDPIdle: 100 * time.Millisecond})

	conn, err := client.DialUDP(dst)
	if err != nil {
		t.Fatalf("DialUDP: %v", err)
	}
	defer func() { _ = conn.Close() }()

	if err := conn.WriteFrame([]byte("ping")); err != nil {
		t.Fatalf("WriteFrame: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.ReadFrame(); err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}

	// After the idle timeout the helper closes the flow, so the next read
	// returns an end-of-stream error.
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.ReadFrame(); err == nil {
		t.Fatal("ReadFrame after the idle timeout returned no error, want a closed stream")
	}
}

func TestLoopbackControlInfo(t *testing.T) {
	info := ControlInfo{Version: "test", GOOS: "linux", GOARCH: "arm64", Hostname: "gw.example", PID: 4242}
	client := newLoopback(t, &Server{Info: info})
	if got := client.Info(); got != info {
		t.Fatalf("Info() = %+v, want %+v", got, info)
	}
}

func TestLoopbackQuit(t *testing.T) {
	client := newLoopback(t, &Server{})
	if err := client.Quit(); err != nil {
		t.Fatalf("Quit: %v", err)
	}
	select {
	case <-client.Wait():
	case <-time.After(2 * time.Second):
		t.Fatal("session did not end after quit")
	}
}
