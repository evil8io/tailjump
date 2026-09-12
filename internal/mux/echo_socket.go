package mux

import (
	"crypto/rand"
	"encoding/binary"
	"net"
	"net/netip"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ICMP message layout: type, code, checksum, identifier, sequence, payload.
// An ICMP error quotes the original IP header and the start of its payload
// after its own 8 byte header.
const (
	icmpHeaderLen = 8

	icmpv4EchoRequest byte = 8
	icmpv4EchoReply   byte = 0
	icmpv6EchoRequest byte = 128
	icmpv6EchoReply   byte = 129

	ipv6HeaderLen = 40

	pingGroupRangePath = "/proc/sys/net/ipv4/ping_group_range"
)

// echoSocket sends echo requests from the remote and reads the replies and
// the ICMP errors for them. A raw ICMP socket needs CAP_NET_RAW; the helper
// sets the identifier itself, and the socket receives every ICMP message of
// the host, errors included. A ping socket, SOCK_DGRAM with the ICMP
// protocol, needs the sysctl net.ipv4.ping_group_range to include the
// helper's group; the kernel sets the identifier, delivers only the matching
// replies, and reports the errors through the error queue.
type echoSocket struct {
	pc    net.PacketConn
	v6    bool
	raw   bool
	ident uint16
	ttl   ttlSetter
	// pending holds the errors the last recv drained from the error queue.
	// Only the recv caller touches it.
	pending []ICMPError
}

// echoEvent is one thing an echo socket received: a reply, or an error.
type echoEvent struct {
	seq     uint16
	payload []byte
	err     *ICMPError
}

// openEchoSocket opens the first socket that works: raw, then ping. It
// returns an error when neither opens.
func openEchoSocket(v6 bool) (*echoSocket, error) {
	if pc, err := listenRawICMP(v6); err == nil {
		return &echoSocket{pc: pc, v6: v6, raw: true, ident: randomIdent(), ttl: ttlSetter{conn: pc, v6: v6}}, nil
	}
	pc, err := listenPingSocket(v6)
	if err != nil {
		return nil, err
	}
	_ = enableRecvErr(pc, v6)
	return &echoSocket{pc: pc, v6: v6, ttl: ttlSetter{conn: pc, v6: v6}}, nil
}

func listenRawICMP(v6 bool) (net.PacketConn, error) {
	if v6 {
		return net.ListenPacket("ip6:58", "::")
	}
	return net.ListenPacket("ip4:1", "0.0.0.0")
}

// listenPingSocket opens a datagram ICMP socket and hands it to the net
// package, which then treats it as a UDP socket whose port is the
// identifier.
func listenPingSocket(v6 bool) (net.PacketConn, error) {
	family, proto := syscall.AF_INET, syscall.IPPROTO_ICMP
	var sa syscall.Sockaddr = &syscall.SockaddrInet4{}
	if v6 {
		family, proto = syscall.AF_INET6, syscall.IPPROTO_ICMPV6
		sa = &syscall.SockaddrInet6{}
	}
	syscall.ForkLock.RLock()
	fd, err := syscall.Socket(family, syscall.SOCK_DGRAM, proto)
	if err == nil {
		syscall.CloseOnExec(fd)
	}
	syscall.ForkLock.RUnlock()
	if err != nil {
		return nil, err
	}
	if err := syscall.Bind(fd, sa); err != nil {
		_ = syscall.Close(fd)
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "ping")
	defer func() { _ = f.Close() }()
	return net.FilePacketConn(f)
}

func randomIdent() uint16 {
	var b [2]byte
	_, _ = rand.Read(b[:])
	return binary.BigEndian.Uint16(b[:])
}

func (s *echoSocket) Close() error {
	return s.pc.Close()
}

// send writes one echo request to dst with the TTL. The kernel computes the
// checksum on a ping socket and on a raw ICMPv6 socket; a raw ICMPv4 socket
// needs it from the sender, so the sender always fills it in. A send that
// consumes a pending socket error returns the ICMP errors behind it; the
// caller forwards them. On a raw socket the error message itself arrives as
// data, so the errno is dropped.
func (s *echoSocket) send(dst netip.Addr, seq uint16, ttl uint8, payload []byte) ([]ICMPError, error) {
	if err := s.ttl.set(ttl); err != nil {
		return nil, err
	}
	msg := make([]byte, icmpHeaderLen+len(payload))
	msg[0] = icmpv4EchoRequest
	if s.v6 {
		msg[0] = icmpv6EchoRequest
	}
	binary.BigEndian.PutUint16(msg[4:6], s.ident)
	binary.BigEndian.PutUint16(msg[6:8], seq)
	copy(msg[icmpHeaderLen:], payload)
	if !s.v6 {
		binary.BigEndian.PutUint16(msg[2:4], icmpChecksum(msg))
	}
	_, err := s.pc.WriteTo(msg, s.addrFor(dst))
	if err == nil || !isICMPErrno(err) {
		return nil, err
	}
	if s.raw {
		return nil, nil
	}
	return drainErrQueue(s.pc, s.v6), nil
}

func (s *echoSocket) addrFor(dst netip.Addr) net.Addr {
	ip := dst.AsSlice()
	if s.raw {
		return &net.IPAddr{IP: ip}
	}
	return &net.UDPAddr{IP: ip}
}

// recv reads until it has an echo reply or an ICMP error for this socket.
func (s *echoSocket) recv(buf []byte) (echoEvent, error) {
	reply := icmpv4EchoReply
	if s.v6 {
		reply = icmpv6EchoReply
	}
	emptyDrains := 0
	for {
		if len(s.pending) > 0 {
			e := s.pending[0]
			s.pending = s.pending[1:]
			return echoEvent{err: &e}, nil
		}
		n, from, err := s.pc.ReadFrom(buf)
		if err != nil {
			if !isICMPErrno(err) {
				return echoEvent{}, err
			}
			if s.raw {
				continue
			}
			s.pending = drainErrQueue(s.pc, s.v6)
			if len(s.pending) == 0 {
				emptyDrains++
				if emptyDrains > 8 {
					return echoEvent{}, err
				}
			}
			continue
		}
		emptyDrains = 0
		msg := buf[:n]
		if s.raw {
			if e, ok := s.rawError(msg, from); ok {
				return echoEvent{err: &e}, nil
			}
		}
		if len(msg) < icmpHeaderLen || msg[0] != reply || msg[1] != 0 {
			continue
		}
		if s.raw && binary.BigEndian.Uint16(msg[4:6]) != s.ident {
			continue
		}
		return echoEvent{seq: binary.BigEndian.Uint16(msg[6:8]), payload: msg[icmpHeaderLen:]}, nil
	}
}

// rawError parses an ICMP error a raw socket received and returns it when
// the quoted packet is an echo request of this socket. The quoted packet
// starts after the 8 byte error header: an IPv4 header of IHL words, or a
// 40 byte IPv6 header, then the ICMP header with the identifier.
func (s *echoSocket) rawError(msg []byte, from net.Addr) (ICMPError, bool) {
	if len(msg) < icmpHeaderLen || !isICMPErrorType(s.v6, msg[0]) {
		return ICMPError{}, false
	}
	quoted := msg[icmpHeaderLen:]
	var inner []byte
	if s.v6 {
		if len(quoted) < ipv6HeaderLen+icmpHeaderLen || quoted[6] != 58 {
			return ICMPError{}, false
		}
		inner = quoted[ipv6HeaderLen:]
		if inner[0] != icmpv6EchoRequest {
			return ICMPError{}, false
		}
	} else {
		if len(quoted) < 1 {
			return ICMPError{}, false
		}
		ihl := int(quoted[0]&0x0f) * 4
		if ihl < 20 || len(quoted) < ihl+icmpHeaderLen || quoted[9] != 1 {
			return ICMPError{}, false
		}
		inner = quoted[ihl:]
		if inner[0] != icmpv4EchoRequest {
			return ICMPError{}, false
		}
	}
	if binary.BigEndian.Uint16(inner[4:6]) != s.ident {
		return ICMPError{}, false
	}
	ipAddr, ok := from.(*net.IPAddr)
	if !ok {
		return ICMPError{}, false
	}
	src, ok := netip.AddrFromSlice(ipAddr.IP)
	if !ok {
		return ICMPError{}, false
	}
	return ICMPError{
		Type:  msg[0],
		Code:  msg[1],
		Info:  binary.BigEndian.Uint32(msg[4:8]),
		From:  src.Unmap(),
		Inner: append([]byte(nil), inner...),
	}, true
}

// isICMPErrorType reports whether an ICMP type is an error that quotes the
// original packet: Destination Unreachable, Time Exceeded, and Parameter
// Problem for IPv4, and those plus Packet Too Big for IPv6.
func isICMPErrorType(v6 bool, typ byte) bool {
	if v6 {
		return typ >= 1 && typ <= 4
	}
	return typ == 3 || typ == 11 || typ == 12
}

// icmpChecksum is the RFC 1071 checksum over the message, with the checksum
// field itself read as zero.
func icmpChecksum(msg []byte) uint16 {
	var sum uint32
	for i := 0; i+1 < len(msg); i += 2 {
		if i == 2 {
			continue
		}
		sum += uint32(msg[i])<<8 | uint32(msg[i+1])
	}
	if len(msg)%2 == 1 {
		sum += uint32(msg[len(msg)-1]) << 8
	}
	for sum>>16 != 0 {
		sum = sum&0xffff + sum>>16
	}
	return ^uint16(sum)
}

// ttlSetter sets the TTL of a socket before a send, and skips the call when
// the TTL did not change.
type ttlSetter struct {
	conn net.PacketConn
	v6   bool
	last int
}

func (t *ttlSetter) set(ttl uint8) error {
	if int(ttl) == t.last {
		return nil
	}
	sc, ok := t.conn.(syscall.Conn)
	if !ok {
		return nil
	}
	rc, err := sc.SyscallConn()
	if err != nil {
		return err
	}
	var serr error
	err = rc.Control(func(fd uintptr) {
		if t.v6 {
			serr = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IPV6, syscall.IPV6_UNICAST_HOPS, int(ttl))
		} else {
			serr = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IP, syscall.IP_TTL, int(ttl))
		}
	})
	if err != nil {
		return err
	}
	if serr != nil {
		return serr
	}
	t.last = int(ttl)
	return nil
}

// icmpErrorsFor returns the ICMP errors behind a socket error, read from
// the error queue, or nil when the error is not one an ICMP message causes.
func icmpErrorsFor(pc net.PacketConn, v6 bool, err error) []ICMPError {
	if !isICMPErrno(err) {
		return nil
	}
	return drainErrQueue(pc, v6)
}

// echoTimes records the send time per sequence, for the round-trip time in
// the reply frame. It forgets every entry when it grows past the cap, so
// unanswered requests do not accumulate.
type echoTimes struct {
	mu   sync.Mutex
	sent map[uint16]time.Time
}

const echoTimesCap = 1024

func (t *echoTimes) mark(seq uint16) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.sent == nil || len(t.sent) >= echoTimesCap {
		t.sent = make(map[uint16]time.Time)
	}
	t.sent[seq] = time.Now()
}

func (t *echoTimes) take(seq uint16) time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	at, ok := t.sent[seq]
	if !ok {
		return 0
	}
	delete(t.sent, seq)
	return time.Since(at)
}

// probeEchoSocket reports which echo socket the remote offers, for the icmp
// control verb, with the ping_group_range value.
func probeEchoSocket() EchoSocketInfo {
	info := EchoSocketInfo{Socket: EchoSocketNone, PingGroupRange: readPingGroupRange()}
	if pc, err := listenRawICMP(false); err == nil {
		_ = pc.Close()
		info.Socket = EchoSocketRaw
		return info
	}
	if pc, err := listenPingSocket(false); err == nil {
		_ = pc.Close()
		info.Socket = EchoSocketPing
	}
	return info
}

// readPingGroupRange returns the sysctl as "<low>-<high>", or an empty
// string when the file is absent.
func readPingGroupRange() string {
	b, err := os.ReadFile(pingGroupRangePath)
	if err != nil {
		return ""
	}
	return strings.Join(strings.Fields(string(b)), "-")
}
