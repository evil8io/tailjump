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
	if err := conn.WriteDatagram(64, want); err != nil {
		t.Fatalf("WriteDatagram: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	got, err := conn.ReadReply()
	if err != nil {
		t.Fatalf("ReadReply: %v", err)
	}
	if !bytes.Equal(got.Payload, want) {
		t.Fatalf("echo = %q, want %q", got.Payload, want)
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

	if err := conn.WriteDatagram(64, []byte("ping")); err != nil {
		t.Fatalf("WriteDatagram: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.ReadReply(); err != nil {
		t.Fatalf("ReadReply: %v", err)
	}

	// After the idle timeout the helper closes the flow, so the next read
	// returns an end-of-stream error.
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.ReadReply(); err == nil {
		t.Fatal("ReadReply after the idle timeout returned no error, want a closed stream")
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

// echoSkipReason returns the reason to skip an echo test on this host: the
// test runner has neither CAP_NET_RAW nor a ping socket. The ping socket
// needs the runner's group inside net.ipv4.ping_group_range.
func echoSkipReason(t *testing.T, client *Client, dst netip.Addr) string {
	t.Helper()
	conn, err := client.DialICMP(dst, 1)
	if err != nil {
		t.Fatalf("DialICMP: %v", err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	err = conn.ReadStatus()
	if errors.Is(err, ErrEchoUnsupported) {
		return "no raw socket and no ping socket for the test runner (ping_group_range " + readPingGroupRange() + ")"
	}
	if err != nil {
		t.Fatalf("ReadStatus: %v", err)
	}
	return ""
}

func TestLoopbackEchoV4(t *testing.T) {
	testLoopbackEcho(t, netip.MustParseAddr("127.0.0.1"))
}

func TestLoopbackEchoV6(t *testing.T) {
	if !hasIPv6Loopback() {
		t.Skip("no IPv6 loopback on this host")
	}
	testLoopbackEcho(t, netip.MustParseAddr("::1"))
}

// testLoopbackEcho pings the loopback address through the helper and checks
// that the reply carries the sequence and the payload.
func testLoopbackEcho(t *testing.T, dst netip.Addr) {
	client := newLoopback(t, &Server{})
	if reason := echoSkipReason(t, client, dst); reason != "" {
		t.Skip(reason)
	}

	conn, err := client.DialICMP(dst, 0x1234)
	if err != nil {
		t.Fatalf("DialICMP: %v", err)
	}
	defer func() { _ = conn.Close() }()

	want := []byte("echo over the mux")
	for seq := uint16(1); seq <= 2; seq++ {
		if err := conn.WriteRequest(seq, 64, want); err != nil {
			t.Fatalf("WriteRequest: %v", err)
		}
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if err := conn.ReadStatus(); err != nil {
		t.Fatalf("ReadStatus: %v", err)
	}
	for seq := uint16(1); seq <= 2; seq++ {
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		reply, err := conn.ReadReply()
		if err != nil {
			t.Fatalf("ReadReply: %v", err)
		}
		if reply.Error != nil {
			t.Fatalf("reply is an icmp error: %+v", *reply.Error)
		}
		if reply.Seq != seq || !bytes.Equal(reply.Payload, want) {
			t.Fatalf("reply = seq %d payload %q, want seq %d payload %q", reply.Seq, reply.Payload, seq, want)
		}
	}
}

func hasIPv6Loopback() bool {
	ln, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}

func TestLoopbackEchoSocketInfo(t *testing.T) {
	client := newLoopback(t, &Server{})
	info, err := client.QueryEchoSocket()
	if err != nil {
		t.Fatalf("QueryEchoSocket: %v", err)
	}
	switch info.Socket {
	case EchoSocketRaw, EchoSocketPing, EchoSocketNone:
	default:
		t.Fatalf("socket = %q", info.Socket)
	}
	if info.PingGroupRange == "" {
		t.Fatal("ping_group_range is empty on Linux")
	}
}

// TestLoopbackUDPUnreachable sends a datagram to a closed loopback port. The
// kernel answers with a Port Unreachable, the helper reads it from the error
// queue, and the flow returns it as an error frame instead of closing.
func TestLoopbackUDPUnreachable(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dst := pc.LocalAddr().(*net.UDPAddr).AddrPort()
	_ = pc.Close()

	client := newLoopback(t, &Server{})
	conn, err := client.DialUDP(dst)
	if err != nil {
		t.Fatalf("DialUDP: %v", err)
	}
	defer func() { _ = conn.Close() }()

	if err := conn.WriteDatagram(64, []byte("probe")); err != nil {
		t.Fatalf("WriteDatagram: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	reply, err := conn.ReadReply()
	if err != nil {
		t.Fatalf("ReadReply: %v", err)
	}
	if reply.Error == nil {
		t.Fatalf("reply = %+v, want an icmp error", reply)
	}
	e := *reply.Error
	if e.Type != 3 || e.Code != 3 || e.From != netip.MustParseAddr("127.0.0.1") {
		t.Fatalf("icmp error = type %d code %d from %s, want port unreachable from 127.0.0.1", e.Type, e.Code, e.From)
	}
	if !bytes.Equal(e.Inner, []byte("probe")) {
		t.Fatalf("quoted payload = %q, want the datagram", e.Inner)
	}

	// The flow is still open: a second probe gets a second error.
	if err := conn.WriteDatagram(64, []byte("again")); err != nil {
		t.Fatalf("second WriteDatagram: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	reply, err = conn.ReadReply()
	if err != nil || reply.Error == nil {
		t.Fatalf("second ReadReply = %+v %v, want an icmp error", reply, err)
	}
}
