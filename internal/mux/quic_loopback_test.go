package mux

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/evil8io/tailjump/internal/transport"
)

// freePortRange returns a range of ten ports that starts at a port the
// kernel just handed out, so the helper listener in the test binds one of
// them.
func freePortRange(t *testing.T) transport.PortRange {
	t.Helper()
	pc, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	port := pc.LocalAddr().(*net.UDPAddr).Port
	_ = pc.Close()
	return transport.PortRange{First: uint16(port), Last: uint16(port + 9)}
}

// newQUICLoopback negotiates the QUIC transport over a yamux loopback and
// dials it, all on 127.0.0.1 in one process.
func newQUICLoopback(t *testing.T, srv *Server) (*Client, *QUICClient, QUICRequest, QUICReply) {
	t.Helper()
	client := newLoopback(t, srv)
	cert, fp, err := NewClientCertificate()
	if err != nil {
		t.Fatal(err)
	}
	req := QUICRequest{
		BindAddr:    netip.MustParseAddr("127.0.0.1"),
		Ports:       freePortRange(t),
		Controller:  transport.ControllerFor(0),
		Fingerprint: fp,
	}
	reply, err := client.NegotiateQUIC(req)
	if err != nil {
		t.Fatalf("NegotiateQUIC: %v", err)
	}
	if !req.Ports.Contains(reply.Port) {
		t.Fatalf("helper bound port %d outside %s", reply.Port, req.Ports)
	}
	q, err := DialQUIC(context.Background(), netip.AddrPortFrom(req.BindAddr, reply.Port), cert, reply.Fingerprint, req.Controller)
	if err != nil {
		t.Fatalf("DialQUIC: %v", err)
	}
	t.Cleanup(func() { _ = q.Close() })
	return client, q, req, reply
}

func TestQUICLoopbackTCP(t *testing.T) {
	dst := tcpEchoListener(t)
	_, q, _, _ := newQUICLoopback(t, &Server{})

	conn, err := q.DialTCP(dst)
	if err != nil {
		t.Fatalf("DialTCP: %v", err)
	}
	want := bytes.Repeat([]byte("hello over quic "), 4096)
	go func() {
		_, _ = conn.Write(want)
		_ = conn.Close()
	}()
	got, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("echo returned %d bytes, want %d", len(got), len(want))
	}
	_ = conn.Close()
}

func TestQUICLoopbackTCPRefused(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dst := ln.Addr().(*net.TCPAddr).AddrPort()
	_ = ln.Close()

	_, q, _, _ := newQUICLoopback(t, &Server{})
	if _, err := q.DialTCP(dst); err == nil {
		t.Fatal("DialTCP to a closed port returned no error")
	}
}

func TestQUICLoopbackUDP(t *testing.T) {
	dst := udpEchoServer(t)
	_, q, _, _ := newQUICLoopback(t, &Server{})

	conn, err := q.DialUDP(dst)
	if err != nil {
		t.Fatalf("DialUDP: %v", err)
	}
	defer func() { _ = conn.Close() }()

	want := []byte("ping over quic")
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

func TestQUICLoopbackSecondConnectionRefused(t *testing.T) {
	_, _, req, reply := newQUICLoopback(t, &Server{})
	cert, _, err := NewClientCertificate()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if q, err := DialQUIC(ctx, netip.AddrPortFrom(req.BindAddr, reply.Port), cert, reply.Fingerprint, req.Controller); err == nil {
		_ = q.Close()
		t.Fatal("a second connection was accepted")
	}
}

func TestQUICLoopbackWrongFingerprints(t *testing.T) {
	client := newLoopback(t, &Server{})
	cert, fp, err := NewClientCertificate()
	if err != nil {
		t.Fatal(err)
	}
	req := QUICRequest{
		BindAddr:    netip.MustParseAddr("127.0.0.1"),
		Ports:       freePortRange(t),
		Controller:  transport.ControllerFor(0),
		Fingerprint: fp,
	}
	reply, err := client.NegotiateQUIC(req)
	if err != nil {
		t.Fatalf("NegotiateQUIC: %v", err)
	}
	addr := netip.AddrPortFrom(req.BindAddr, reply.Port)

	// The client pins the wrong helper fingerprint.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if q, err := DialQUIC(ctx, addr, cert, testFP, req.Controller); err == nil {
		_ = q.Close()
		t.Fatal("a dial with the wrong helper fingerprint succeeded")
	}

	// The client presents a certificate the helper did not pin.
	other, _, err := NewClientCertificate()
	if err != nil {
		t.Fatal(err)
	}
	if q, err := DialQUIC(ctx, addr, other, reply.Fingerprint, req.Controller); err == nil {
		_ = q.Close()
		t.Fatal("a dial with an unpinned client certificate succeeded")
	}

	// The pinned pair still works after the refusals.
	q, err := DialQUIC(context.Background(), addr, cert, reply.Fingerprint, req.Controller)
	if err != nil {
		t.Fatalf("DialQUIC with the pinned pair: %v", err)
	}
	_ = q.Close()
}

func TestQUICLoopbackAbandonFreesPort(t *testing.T) {
	client := newLoopback(t, &Server{})
	_, fp, err := NewClientCertificate()
	if err != nil {
		t.Fatal(err)
	}
	req := QUICRequest{
		BindAddr:    netip.MustParseAddr("127.0.0.1"),
		Ports:       freePortRange(t),
		Controller:  transport.ControllerFor(0),
		Fingerprint: fp,
	}
	reply, err := client.NegotiateQUIC(req)
	if err != nil {
		t.Fatalf("NegotiateQUIC: %v", err)
	}
	if err := client.AbandonQUIC(); err != nil {
		t.Fatalf("AbandonQUIC: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		pc, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(reply.Port)})
		if err == nil {
			_ = pc.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("port %d is still bound after abandon: %v", reply.Port, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, err := client.NegotiateQUIC(req); err != nil {
		t.Fatalf("NegotiateQUIC after abandon: %v", err)
	}
}

func TestQUICLoopbackControlKindRefused(t *testing.T) {
	_, q, _, _ := newQUICLoopback(t, &Server{})
	stream, err := q.openStream(kindControl, netip.MustParseAddrPort("127.0.0.1:1"))
	if err != nil {
		t.Fatalf("openStream: %v", err)
	}
	_ = stream.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := readStatus(stream); err == nil {
		t.Fatal("a control stream over quic was answered")
	}
}

func TestQUICLoopbackQuitClosesListener(t *testing.T) {
	client, q, req, reply := newQUICLoopback(t, &Server{})
	if err := client.Quit(); err != nil {
		t.Fatalf("Quit: %v", err)
	}
	select {
	case <-q.Wait():
	case <-time.After(3 * time.Second):
		t.Fatal("the quic connection did not end after quit")
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		pc, err := net.ListenUDP("udp", net.UDPAddrFromAddrPort(netip.AddrPortFrom(req.BindAddr, reply.Port)))
		if err == nil {
			_ = pc.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("port %d is still bound after quit: %v", reply.Port, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
