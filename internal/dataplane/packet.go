package dataplane

import (
	"encoding/binary"
	"net/netip"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/checksum"
	"gvisor.dev/gvisor/pkg/tcpip/header"

	"github.com/evil8io/tailjump/internal/mux"
)

// The packets the client builds itself carry this TTL. The remote's reply
// TTL is not known on the client.
const defaultTTL = 64

// ICMP error packet size caps: RFC 1812 section 4.3.2.3 for IPv4, and the
// IPv6 minimum MTU of RFC 4443 section 2.4.
const (
	icmpv4ErrorMaxSize = 576
	icmpv6ErrorMaxSize = 1280
)

// The echo request types and the identifier offset in an ICMP header, for
// the quoted-identifier rewrite.
const (
	icmpv4EchoType  byte = 8
	icmpv6EchoType  byte = 128
	icmpIdentOffset      = 4
)

// buildEchoReply builds the echo reply packet for a captured request: from
// the pinged address to the original source, with the identifier, the
// sequence, and the payload the remote received.
func buildEchoReply(pinged, to netip.Addr, ident, seq uint16, payload []byte) []byte {
	if to.Is4() {
		pkt := make([]byte, header.IPv4MinimumSize+header.ICMPv4MinimumSize+len(payload))
		icmp := header.ICMPv4(pkt[header.IPv4MinimumSize:])
		icmp.SetType(header.ICMPv4EchoReply)
		icmp.SetCode(0)
		icmp.SetIdent(ident)
		icmp.SetSequence(seq)
		copy(icmp.Payload(), payload)
		icmp.SetChecksum(header.ICMPv4Checksum(icmp, 0))
		encodeIPv4(pkt, pinged, to, header.ICMPv4ProtocolNumber)
		return pkt
	}
	pkt := make([]byte, header.IPv6MinimumSize+header.ICMPv6EchoMinimumSize+len(payload))
	icmp := header.ICMPv6(pkt[header.IPv6MinimumSize:])
	icmp.SetType(header.ICMPv6EchoReply)
	icmp.SetCode(0)
	icmp.SetIdent(ident)
	icmp.SetSequence(seq)
	copy(icmp.Payload(), payload)
	icmp.SetChecksum(header.ICMPv6Checksum(header.ICMPv6ChecksumParams{
		Header: icmp,
		Src:    tcpipAddr(pinged),
		Dst:    tcpipAddr(to),
	}))
	encodeIPv6(pkt, pinged, to, header.ICMPv6ProtocolNumber)
	return pkt
}

// buildICMPError builds the ICMP error packet for a flow: from the hop that
// sent the error to the original source, with the type, the code, and the
// info word of the error, and the rebuilt original packet as the quoted
// part, cut to the size cap of the address family.
func buildICMPError(e mux.ICMPError, to netip.Addr, inner []byte) []byte {
	if to.Is4() {
		inner = cut(inner, icmpv4ErrorMaxSize-header.IPv4MinimumSize-header.ICMPv4MinimumSize)
		pkt := make([]byte, header.IPv4MinimumSize+header.ICMPv4MinimumSize+len(inner))
		icmp := header.ICMPv4(pkt[header.IPv4MinimumSize:])
		icmp.SetType(header.ICMPv4Type(e.Type))
		icmp.SetCode(header.ICMPv4Code(e.Code))
		binary.BigEndian.PutUint32(icmp[4:8], e.Info)
		copy(icmp.Payload(), inner)
		icmp.SetChecksum(header.ICMPv4Checksum(icmp, 0))
		encodeIPv4(pkt, e.From, to, header.ICMPv4ProtocolNumber)
		return pkt
	}
	inner = cut(inner, icmpv6ErrorMaxSize-header.IPv6MinimumSize-header.ICMPv6MinimumSize)
	pkt := make([]byte, header.IPv6MinimumSize+header.ICMPv6MinimumSize+len(inner))
	icmp := header.ICMPv6(pkt[header.IPv6MinimumSize:])
	icmp.SetType(header.ICMPv6Type(e.Type))
	icmp.SetCode(header.ICMPv6Code(e.Code))
	icmp.SetTypeSpecific(e.Info)
	copy(icmp.Payload(), inner)
	icmp.SetChecksum(header.ICMPv6Checksum(header.ICMPv6ChecksumParams{
		Header: icmp,
		Src:    tcpipAddr(e.From),
		Dst:    tcpipAddr(to),
	}))
	encodeIPv6(pkt, e.From, to, header.ICMPv6ProtocolNumber)
	return pkt
}

// innerICMP rebuilds the original echo request as an ICMP error quotes it:
// the IP header from the source to the pinged address, then the ICMP bytes
// the remote's error quoted.
func innerICMP(src, dst netip.Addr, icmpBytes []byte) []byte {
	if src.Is4() {
		pkt := make([]byte, header.IPv4MinimumSize+len(icmpBytes))
		copy(pkt[header.IPv4MinimumSize:], icmpBytes)
		encodeIPv4(pkt, src, dst, header.ICMPv4ProtocolNumber)
		return pkt
	}
	pkt := make([]byte, header.IPv6MinimumSize+len(icmpBytes))
	copy(pkt[header.IPv6MinimumSize:], icmpBytes)
	encodeIPv6(pkt, src, dst, header.ICMPv6ProtocolNumber)
	return pkt
}

// innerUDP rebuilds the original datagram as an ICMP error quotes it: the IP
// header, the UDP header with the ports of the flow and the length of the
// last datagram sent, then the payload bytes the remote's error quoted.
func innerUDP(src, dst netip.AddrPort, lastLen int, payload []byte) []byte {
	ipLen := header.IPv6MinimumSize
	if src.Addr().Is4() {
		ipLen = header.IPv4MinimumSize
	}
	pkt := make([]byte, ipLen+header.UDPMinimumSize+len(payload))
	udp := header.UDP(pkt[ipLen:])
	udp.Encode(&header.UDPFields{
		SrcPort: src.Port(),
		DstPort: dst.Port(),
		Length:  uint16(header.UDPMinimumSize + lastLen),
	})
	copy(udp.Payload(), payload)
	if src.Addr().Is4() {
		encodeIPv4(pkt, src.Addr(), dst.Addr(), header.UDPProtocolNumber)
	} else {
		encodeIPv6(pkt, src.Addr(), dst.Addr(), header.UDPProtocolNumber)
	}
	return pkt
}

func encodeIPv4(pkt []byte, src, dst netip.Addr, proto tcpip.TransportProtocolNumber) {
	ip := header.IPv4(pkt)
	ip.Encode(&header.IPv4Fields{
		TotalLength: uint16(len(pkt)),
		TTL:         defaultTTL,
		Protocol:    uint8(proto),
		SrcAddr:     tcpipAddr(src),
		DstAddr:     tcpipAddr(dst),
	})
	ip.SetChecksum(^ip.CalculateChecksum())
}

func encodeIPv6(pkt []byte, src, dst netip.Addr, proto tcpip.TransportProtocolNumber) {
	ip := header.IPv6(pkt)
	ip.Encode(&header.IPv6Fields{
		PayloadLength:     uint16(len(pkt) - header.IPv6MinimumSize),
		TransportProtocol: proto,
		HopLimit:          defaultTTL,
		SrcAddr:           tcpipAddr(src),
		DstAddr:           tcpipAddr(dst),
	})
}

// patchQuotedEchoIdent rewrites the identifier of an echo request that an
// ICMP error quotes back to the client's identifier. The remote's socket
// sends echo requests under its own identifier, a raw socket's random value
// or a ping socket's kernel port, so a Time Exceeded or an Unreachable from
// a hop quotes that identifier. The client's ping and traceroute match a
// reply to a probe by the quoted identifier, so without the rewrite the
// client drops the error. The sequence is preserved by the remote, so only
// the identifier needs the rewrite. The quoted ICMP checksum stays stale,
// which no matcher reads.
func patchQuotedEchoIdent(inner []byte, ident uint16, v6 bool) {
	if len(inner) < icmpIdentOffset+2 {
		return
	}
	echo := icmpv4EchoType
	if v6 {
		echo = icmpv6EchoType
	}
	if inner[0] != echo {
		return
	}
	binary.BigEndian.PutUint16(inner[icmpIdentOffset:icmpIdentOffset+2], ident)
}

func tcpipAddr(a netip.Addr) tcpip.Address {
	return tcpip.AddrFromSlice(a.Unmap().AsSlice())
}

func cut(b []byte, n int) []byte {
	if len(b) > n {
		return b[:n]
	}
	return b
}

// payloadChecksum is the checksum of a byte slice, for the tests that check
// a built packet.
func payloadChecksum(b []byte) uint16 {
	return checksum.Checksum(b, 0)
}
