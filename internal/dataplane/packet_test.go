package dataplane

import (
	"bytes"
	"net/netip"
	"testing"

	"gvisor.dev/gvisor/pkg/tcpip/header"

	"github.com/evil8io/tailjump/internal/mux"
)

var (
	pingedV4 = netip.MustParseAddr("10.0.0.2")
	sourceV4 = netip.MustParseAddr("169.254.117.1")
	pingedV6 = netip.MustParseAddr("2001:db8::a")
	sourceV6 = netip.MustParseAddr("fd00:117::1")
	hopV4    = netip.MustParseAddr("10.0.0.1")
	hopV6    = netip.MustParseAddr("2001:db8::1")
)

// checkIPv4 validates the header and its checksum and returns the payload.
func checkIPv4(t *testing.T, pkt []byte, src, dst netip.Addr, proto uint8) []byte {
	t.Helper()
	h := header.IPv4(pkt)
	if !h.IsValid(len(pkt)) {
		t.Fatalf("ipv4 header invalid")
	}
	if h.CalculateChecksum() != 0xffff {
		t.Fatalf("ipv4 header checksum = %#x, want a valid sum", h.CalculateChecksum())
	}
	if got := addrFrom(h.SourceAddress()); got != src {
		t.Fatalf("ipv4 src = %s, want %s", got, src)
	}
	if got := addrFrom(h.DestinationAddress()); got != dst {
		t.Fatalf("ipv4 dst = %s, want %s", got, dst)
	}
	if h.Protocol() != proto {
		t.Fatalf("ipv4 protocol = %d, want %d", h.Protocol(), proto)
	}
	if int(h.TotalLength()) != len(pkt) {
		t.Fatalf("ipv4 total length = %d, want %d", h.TotalLength(), len(pkt))
	}
	return h.Payload()
}

// checkIPv6 validates the header and returns the payload.
func checkIPv6(t *testing.T, pkt []byte, src, dst netip.Addr, proto uint8) []byte {
	t.Helper()
	h := header.IPv6(pkt)
	if !h.IsValid(len(pkt)) {
		t.Fatalf("ipv6 header invalid")
	}
	if got := addrFrom(h.SourceAddress()); got != src {
		t.Fatalf("ipv6 src = %s, want %s", got, src)
	}
	if got := addrFrom(h.DestinationAddress()); got != dst {
		t.Fatalf("ipv6 dst = %s, want %s", got, dst)
	}
	if h.NextHeader() != proto {
		t.Fatalf("ipv6 next header = %d, want %d", h.NextHeader(), proto)
	}
	if int(h.PayloadLength()) != len(pkt)-header.IPv6MinimumSize {
		t.Fatalf("ipv6 payload length = %d, want %d", h.PayloadLength(), len(pkt)-header.IPv6MinimumSize)
	}
	return h.Payload()
}

func TestBuildEchoReplyV4(t *testing.T) {
	payload := []byte("0123456789abcdef")
	pkt := buildEchoReply(pingedV4, sourceV4, 0x1234, 7, payload)
	icmp := header.ICMPv4(checkIPv4(t, pkt, pingedV4, sourceV4, uint8(header.ICMPv4ProtocolNumber)))
	if icmp.Type() != header.ICMPv4EchoReply || icmp.Code() != 0 {
		t.Fatalf("icmp type/code = %d/%d", icmp.Type(), icmp.Code())
	}
	if icmp.Ident() != 0x1234 || icmp.Sequence() != 7 {
		t.Fatalf("icmp ident/seq = %#x/%d", icmp.Ident(), icmp.Sequence())
	}
	if !bytes.Equal(icmp.Payload(), payload) {
		t.Fatalf("icmp payload = %q", icmp.Payload())
	}
	// A valid ICMPv4 message sums to 0xffff over its whole length.
	if got := payloadChecksum(icmp); got != 0xffff {
		t.Fatalf("icmpv4 checksum over the message = %#x, want 0xffff", got)
	}
}

func TestBuildEchoReplyV6(t *testing.T) {
	payload := []byte("0123456789abcdef")
	pkt := buildEchoReply(pingedV6, sourceV6, 0x4321, 9, payload)
	icmp := header.ICMPv6(checkIPv6(t, pkt, pingedV6, sourceV6, uint8(header.ICMPv6ProtocolNumber)))
	if icmp.Type() != header.ICMPv6EchoReply || icmp.Code() != 0 {
		t.Fatalf("icmp type/code = %d/%d", icmp.Type(), icmp.Code())
	}
	if icmp.Ident() != 0x4321 || icmp.Sequence() != 9 {
		t.Fatalf("icmp ident/seq = %#x/%d", icmp.Ident(), icmp.Sequence())
	}
	if !bytes.Equal(icmp.Payload(), payload) {
		t.Fatalf("icmp payload = %q", icmp.Payload())
	}
	want := header.ICMPv6Checksum(header.ICMPv6ChecksumParams{Header: icmp, Src: tcpipAddr(pingedV6), Dst: tcpipAddr(sourceV6)})
	if icmp.Checksum() != want {
		t.Fatalf("icmpv6 checksum = %#x, want %#x", icmp.Checksum(), want)
	}
}

// TestBuildICMPErrorV4 rebuilds a Time Exceeded for an echo probe and checks
// the outer packet, the error header, and the quoted original packet.
func TestBuildICMPErrorV4(t *testing.T) {
	quoted := []byte{8, 0, 0xf7, 0xfd, 0x12, 0x34, 0, 3}
	e := mux.ICMPError{Type: 11, Code: 0, From: hopV4, Inner: quoted}
	pkt := buildICMPError(e, sourceV4, innerICMP(sourceV4, pingedV4, quoted))
	icmp := header.ICMPv4(checkIPv4(t, pkt, hopV4, sourceV4, uint8(header.ICMPv4ProtocolNumber)))
	if icmp.Type() != 11 || icmp.Code() != 0 {
		t.Fatalf("icmp type/code = %d/%d", icmp.Type(), icmp.Code())
	}
	if got := payloadChecksum(icmp); got != 0xffff {
		t.Fatalf("icmpv4 checksum over the message = %#x, want 0xffff", got)
	}
	inner := checkIPv4(t, icmp.Payload(), sourceV4, pingedV4, uint8(header.ICMPv4ProtocolNumber))
	if !bytes.Equal(inner, quoted) {
		t.Fatalf("quoted icmp = %v, want %v", inner, quoted)
	}
}

// TestBuildICMPErrorV6 rebuilds a Port Unreachable for a UDP probe and
// checks the quoted UDP header.
func TestBuildICMPErrorV6(t *testing.T) {
	src := netip.AddrPortFrom(sourceV6, 40000)
	dst := netip.AddrPortFrom(pingedV6, 33434)
	e := mux.ICMPError{Type: 1, Code: 4, From: hopV6}
	pkt := buildICMPError(e, sourceV6, innerUDP(src, dst, 32, nil))
	icmp := header.ICMPv6(checkIPv6(t, pkt, hopV6, sourceV6, uint8(header.ICMPv6ProtocolNumber)))
	if icmp.Type() != 1 || icmp.Code() != 4 {
		t.Fatalf("icmp type/code = %d/%d", icmp.Type(), icmp.Code())
	}
	want := header.ICMPv6Checksum(header.ICMPv6ChecksumParams{Header: icmp, Src: tcpipAddr(hopV6), Dst: tcpipAddr(sourceV6)})
	if icmp.Checksum() != want {
		t.Fatalf("icmpv6 checksum = %#x, want %#x", icmp.Checksum(), want)
	}
	udp := header.UDP(checkIPv6(t, icmp.Payload(), sourceV6, pingedV6, uint8(header.UDPProtocolNumber)))
	if udp.SourcePort() != 40000 || udp.DestinationPort() != 33434 || udp.Length() != 8+32 {
		t.Fatalf("quoted udp = %d -> %d length %d", udp.SourcePort(), udp.DestinationPort(), udp.Length())
	}
}

func TestBuildICMPErrorCapsTheQuote(t *testing.T) {
	e := mux.ICMPError{Type: 3, Code: 3, From: hopV4}
	pkt := buildICMPError(e, sourceV4, make([]byte, 2000))
	if len(pkt) != icmpv4ErrorMaxSize {
		t.Fatalf("ipv4 error packet = %d bytes, want %d", len(pkt), icmpv4ErrorMaxSize)
	}
	e = mux.ICMPError{Type: 3, Code: 0, From: hopV6}
	pkt = buildICMPError(e, sourceV6, make([]byte, 2000))
	if len(pkt) != icmpv6ErrorMaxSize {
		t.Fatalf("ipv6 error packet = %d bytes, want %d", len(pkt), icmpv6ErrorMaxSize)
	}
}

func TestBuildICMPErrorPacketTooBigInfo(t *testing.T) {
	e := mux.ICMPError{Type: 2, Code: 0, Info: 1280, From: hopV6}
	pkt := buildICMPError(e, sourceV6, nil)
	icmp := header.ICMPv6(pkt[header.IPv6MinimumSize:])
	if icmp.MTU() != 1280 {
		t.Fatalf("mtu = %d, want 1280", icmp.MTU())
	}
	e = mux.ICMPError{Type: 3, Code: 4, Info: 1400, From: hopV4}
	pkt = buildICMPError(e, sourceV4, nil)
	icmp4 := header.ICMPv4(pkt[header.IPv4MinimumSize:])
	if icmp4.MTU() != 1400 {
		t.Fatalf("mtu = %d, want 1400", icmp4.MTU())
	}
}

func TestPatchQuotedEchoIdent(t *testing.T) {
	// A hop quotes the first 8 bytes of the echo request: type 8, code 0,
	// checksum, the remote's identifier, and the sequence.
	inner := []byte{8, 0, 0xaa, 0xbb, 0x99, 0x99, 0, 7}
	patchQuotedEchoIdent(inner, 0x1234, false)
	if inner[4] != 0x12 || inner[5] != 0x34 {
		t.Fatalf("ident = %#x%#x, want 0x1234", inner[4], inner[5])
	}
	if inner[7] != 7 {
		t.Fatalf("sequence changed to %d", inner[7])
	}
	// A non-echo inner, for example a quoted UDP datagram, is left alone.
	udp := []byte{0, 1, 2, 3, 4, 5, 6, 7}
	patchQuotedEchoIdent(udp, 0x1234, false)
	if udp[4] != 4 || udp[5] != 5 {
		t.Fatalf("non-echo inner was modified: %v", udp)
	}
	// IPv6 uses type 128.
	inner6 := []byte{128, 0, 0, 0, 0, 0, 0, 1}
	patchQuotedEchoIdent(inner6, 0xbeef, true)
	if inner6[4] != 0xbe || inner6[5] != 0xef {
		t.Fatalf("v6 ident = %#x%#x, want 0xbeef", inner6[4], inner6[5])
	}
}
