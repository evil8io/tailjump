package dataplane

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/netip"
	"os"
	"strings"
	"testing"
	"time"

	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/stack"

	"github.com/evil8io/tailjump/internal/mux"
	"github.com/evil8io/tailjump/internal/protocols"
)

// echoRequestPacket builds an echo request packet from src to dst, the way
// the kernel writes it to the device.
func echoRequestPacket(src, dst netip.Addr, ident, seq uint16, ttl uint8, payload []byte) []byte {
	if dst.Is4() {
		pkt := make([]byte, header.IPv4MinimumSize+header.ICMPv4MinimumSize+len(payload))
		icmp := header.ICMPv4(pkt[header.IPv4MinimumSize:])
		icmp.SetType(header.ICMPv4Echo)
		icmp.SetIdent(ident)
		icmp.SetSequence(seq)
		copy(icmp.Payload(), payload)
		icmp.SetChecksum(header.ICMPv4Checksum(icmp, 0))
		ip := header.IPv4(pkt)
		ip.Encode(&header.IPv4Fields{
			TotalLength: uint16(len(pkt)),
			TTL:         ttl,
			Protocol:    uint8(header.ICMPv4ProtocolNumber),
			SrcAddr:     tcpipAddr(src),
			DstAddr:     tcpipAddr(dst),
		})
		ip.SetChecksum(^ip.CalculateChecksum())
		return pkt
	}
	pkt := make([]byte, header.IPv6MinimumSize+header.ICMPv6EchoMinimumSize+len(payload))
	icmp := header.ICMPv6(pkt[header.IPv6MinimumSize:])
	icmp.SetType(header.ICMPv6EchoRequest)
	icmp.SetIdent(ident)
	icmp.SetSequence(seq)
	copy(icmp.Payload(), payload)
	icmp.SetChecksum(header.ICMPv6Checksum(header.ICMPv6ChecksumParams{Header: icmp, Src: tcpipAddr(src), Dst: tcpipAddr(dst)}))
	ip := header.IPv6(pkt)
	ip.Encode(&header.IPv6Fields{
		PayloadLength:     uint16(len(pkt) - header.IPv6MinimumSize),
		TransportProtocol: header.ICMPv6ProtocolNumber,
		HopLimit:          ttl,
		SrcAddr:           tcpipAddr(src),
		DstAddr:           tcpipAddr(dst),
	})
	return pkt
}

func TestParseEchoRequest(t *testing.T) {
	pkt := echoRequestPacket(sourceV4, pingedV4, 0x1234, 5, 3, []byte("data"))
	key, req, ok := parseEchoRequest(pkt)
	if !ok {
		t.Fatal("ipv4 echo request not parsed")
	}
	if key.dst != pingedV4 || key.ident != 0x1234 || req.src != sourceV4 || req.seq != 5 || req.ttl != 3 || string(req.payload) != "data" {
		t.Fatalf("ipv4 parse = %+v %+v", key, req)
	}
	pkt = echoRequestPacket(sourceV6, pingedV6, 0x4321, 6, 2, []byte("data6"))
	key, req, ok = parseEchoRequest(pkt)
	if !ok {
		t.Fatal("ipv6 echo request not parsed")
	}
	if key.dst != pingedV6 || key.ident != 0x4321 || req.src != sourceV6 || req.seq != 6 || req.ttl != 2 || string(req.payload) != "data6" {
		t.Fatalf("ipv6 parse = %+v %+v", key, req)
	}

	// An echo reply, a UDP packet, and a truncated packet are not requests.
	if _, _, ok := parseEchoRequest(buildEchoReply(pingedV4, sourceV4, 1, 1, nil)); ok {
		t.Fatal("echo reply parsed as a request")
	}
	if _, _, ok := parseEchoRequest(innerUDP(netip.AddrPortFrom(sourceV4, 1), netip.AddrPortFrom(pingedV4, 2), 0, nil)); ok {
		t.Fatal("udp packet parsed as a request")
	}
	if _, _, ok := parseEchoRequest(pkt[:30]); ok {
		t.Fatal("truncated packet parsed as a request")
	}
}

// echoStack wires a netstack with the given set to an in-process helper and
// returns it without a link pump, so the test reads the packets the
// netstack emits itself.
func echoStack(t *testing.T, set protocols.Set) (*netStack, *mux.Client) {
	t.Helper()
	clientPipe, helperPipe := net.Pipe()
	go func() {
		srv := &mux.Server{Info: mux.ControlInfo{Version: "test", GOOS: "linux", GOARCH: "amd64", Hostname: "helper", PID: 1}}
		_ = srv.Serve(helperPipe)
	}()
	client, err := mux.NewClient(clientPipe)
	if err != nil {
		t.Fatalf("mux client: %v", err)
	}
	ns, err := newNetStackWith(testMTU, client, set, loopbackNetProtos())
	if err != nil {
		t.Fatalf("netstack: %v", err)
	}
	t.Cleanup(func() {
		ns.close()
		_ = client.Close()
	})
	return ns, client
}

func skipWithoutEchoSocket(t *testing.T, client *mux.Client, dst netip.Addr) {
	t.Helper()
	conn, err := client.DialICMP(dst, 1)
	if err != nil {
		t.Fatalf("DialICMP: %v", err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	err = conn.ReadStatus()
	if errors.Is(err, mux.ErrEchoUnsupported) {
		rng, _ := os.ReadFile("/proc/sys/net/ipv4/ping_group_range")
		t.Skipf("no raw socket and no ping socket for the test runner (ping_group_range %s)", strings.TrimSpace(string(rng)))
	}
	if err != nil {
		t.Fatalf("ReadStatus: %v", err)
	}
}

func TestEchoThroughNetstackV4(t *testing.T) {
	testEchoThroughNetstack(t, netip.MustParseAddr("127.0.0.1"), sourceV4)
}

func TestEchoThroughNetstackV6(t *testing.T) {
	if !hasIPv6Loopback() {
		t.Skip("no IPv6 loopback on this host")
	}
	testEchoThroughNetstack(t, netip.MustParseAddr("::1"), sourceV6)
}

// testEchoThroughNetstack feeds an echo request to the capture, as the TUN
// pump does, and reads the reply the flow writes to the link endpoint.
func testEchoThroughNetstack(t *testing.T, dst, src netip.Addr) {
	ns, client := echoStack(t, protocols.All())
	skipWithoutEchoSocket(t, client, dst)

	payload := []byte("ping through the netstack")
	pkt := echoRequestPacket(src, dst, 0xabcd, 3, 64, payload)
	if !ns.captureEcho(pkt) {
		t.Fatal("captureEcho did not take the echo request")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pb := ns.ep.ReadContext(ctx)
	if pb == nil {
		t.Fatal("no reply packet within 5s")
	}
	view := pb.ToView()
	reply := append([]byte(nil), view.AsSlice()...)
	view.Release()
	pb.DecRef()

	if dst.Is4() {
		icmp := header.ICMPv4(checkIPv4(t, reply, dst, src, uint8(header.ICMPv4ProtocolNumber)))
		if icmp.Type() != header.ICMPv4EchoReply || icmp.Ident() != 0xabcd || icmp.Sequence() != 3 || !bytes.Equal(icmp.Payload(), payload) {
			t.Fatalf("reply = type %d ident %#x seq %d payload %q", icmp.Type(), icmp.Ident(), icmp.Sequence(), icmp.Payload())
		}
		return
	}
	icmp := header.ICMPv6(checkIPv6(t, reply, dst, src, uint8(header.ICMPv6ProtocolNumber)))
	if icmp.Type() != header.ICMPv6EchoReply || icmp.Ident() != 0xabcd || icmp.Sequence() != 3 || !bytes.Equal(icmp.Payload(), payload) {
		t.Fatalf("reply = type %d ident %#x seq %d payload %q", icmp.Type(), icmp.Ident(), icmp.Sequence(), icmp.Payload())
	}
}

// TestEchoDroppedWithoutICMP checks that a set without icmp consumes the
// request and emits nothing.
func TestEchoDroppedWithoutICMP(t *testing.T) {
	ns, _ := echoStack(t, protocols.Set{TCP: true, UDP: true})
	pkt := echoRequestPacket(sourceV4, netip.MustParseAddr("127.0.0.1"), 1, 1, 64, nil)
	if !ns.captureEcho(pkt) {
		t.Fatal("captureEcho did not consume the echo request")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if pb := ns.ep.ReadContext(ctx); pb != nil {
		pb.DecRef()
		t.Fatal("a packet was emitted for a dropped echo request")
	}
	if len(ns.echoFlows) != 0 {
		t.Fatalf("echo flows = %d, want none", len(ns.echoFlows))
	}
}

// injectAndRead feeds one packet to the netstack as the TUN pump does and
// returns the first packet the netstack emits, or nil after the timeout.
func injectAndRead(t *testing.T, ns *netStack, pkt []byte, timeout time.Duration) []byte {
	t.Helper()
	pn, ok := protoOf(pkt)
	if !ok {
		t.Fatal("packet has no IP version")
	}
	pb := stack.NewPacketBuffer(stack.PacketBufferOptions{Payload: buffer.MakeWithData(pkt)})
	ns.ep.InjectInbound(pn, pb)
	pb.DecRef()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out := ns.ep.ReadContext(ctx)
	if out == nil {
		return nil
	}
	view := out.ToView()
	reply := append([]byte(nil), view.AsSlice()...)
	view.Release()
	out.DecRef()
	return reply
}

// TestUDPUnreachableWithoutUDP checks the enforcement of a set without udp:
// the netstack has no UDP forwarder, so it answers a datagram with a Port
// Unreachable from the destination address.
func TestUDPUnreachableWithoutUDP(t *testing.T) {
	ns, _ := echoStack(t, protocols.Set{TCP: true, ICMP: true})
	src := netip.AddrPortFrom(sourceV4, 40000)
	dst := netip.AddrPortFrom(pingedV4, 33434)
	reply := injectAndRead(t, ns, innerUDP(src, dst, 5, []byte("probe")), 5*time.Second)
	if reply == nil {
		t.Fatal("no reply within 5s, want a port unreachable")
	}
	icmp := header.ICMPv4(checkIPv4(t, reply, pingedV4, sourceV4, uint8(header.ICMPv4ProtocolNumber)))
	if icmp.Type() != header.ICMPv4DstUnreachable || icmp.Code() != header.ICMPv4PortUnreachable {
		t.Fatalf("reply = type %d code %d, want port unreachable", icmp.Type(), icmp.Code())
	}
}

// TestTCPResetWithoutTCP checks the enforcement of a set without tcp: the
// netstack has no TCP forwarder, so it answers a SYN with a reset.
func TestTCPResetWithoutTCP(t *testing.T) {
	ns, _ := echoStack(t, protocols.Set{UDP: true, ICMP: true})
	pkt := make([]byte, header.IPv4MinimumSize+header.TCPMinimumSize)
	tcpHdr := header.TCP(pkt[header.IPv4MinimumSize:])
	tcpHdr.Encode(&header.TCPFields{
		SrcPort:    40000,
		DstPort:    22,
		SeqNum:     1,
		DataOffset: header.TCPMinimumSize,
		Flags:      header.TCPFlagSyn,
		WindowSize: 65535,
	})
	xsum := header.PseudoHeaderChecksum(header.TCPProtocolNumber, tcpipAddr(sourceV4), tcpipAddr(pingedV4), header.TCPMinimumSize)
	tcpHdr.SetChecksum(^tcpHdr.CalculateChecksum(xsum))
	encodeIPv4(pkt, sourceV4, pingedV4, header.TCPProtocolNumber)

	reply := injectAndRead(t, ns, pkt, 5*time.Second)
	if reply == nil {
		t.Fatal("no reply within 5s, want a reset")
	}
	rst := header.TCP(checkIPv4(t, reply, pingedV4, sourceV4, uint8(header.TCPProtocolNumber)))
	if !rst.Flags().Contains(header.TCPFlagRst) {
		t.Fatalf("reply flags = %s, want a reset", rst.Flags())
	}
}
