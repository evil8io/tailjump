package mux

import (
	"encoding/binary"
	"fmt"
	"net/netip"
	"time"
)

// Reply kinds, the first byte of every reply frame on a UDP flow and on an
// ICMP echo flow.
const (
	replyData  byte = 0
	replyError byte = 1
)

// ICMPError is an ICMP error the remote received for a packet of a flow: a
// Time Exceeded from a hop on the way, or a Destination Unreachable. Type
// and Code are the ICMP values of the remote's address family. Info is the
// third word of the ICMP header, the MTU of a Packet Too Big. From is the
// sender of the error. Inner is the part of the original packet that the
// error quoted: from the UDP payload on a UDP flow, and from the ICMP
// header on an echo flow.
type ICMPError struct {
	Type  uint8
	Code  uint8
	Info  uint32
	From  netip.Addr
	Inner []byte
}

// UDPReply is one reply frame of a UDP flow: a datagram, or an ICMP error.
type UDPReply struct {
	Payload []byte
	Error   *ICMPError
}

// EchoReply is one reply frame of an echo flow: an echo reply with the
// round-trip time the helper measured, or an ICMP error.
type EchoReply struct {
	Seq     uint16
	RTT     time.Duration
	Payload []byte
	Error   *ICMPError
}

const (
	udpRequestHeader  = 1
	echoRequestHeader = 3
	echoDataHeader    = 6
	icmpErrorHeader   = 7
)

// A UDP request frame is the TTL and the datagram.
func encodeUDPRequest(ttl uint8, payload []byte) []byte {
	buf := make([]byte, udpRequestHeader+len(payload))
	buf[0] = ttl
	copy(buf[udpRequestHeader:], payload)
	return buf
}

func decodeUDPRequest(frame []byte) (ttl uint8, payload []byte, err error) {
	if len(frame) < udpRequestHeader {
		return 0, nil, fmt.Errorf("mux: udp request frame too short: %d bytes", len(frame))
	}
	return frame[0], frame[udpRequestHeader:], nil
}

func encodeUDPData(payload []byte) []byte {
	buf := make([]byte, 1+len(payload))
	buf[0] = replyData
	copy(buf[1:], payload)
	return buf
}

func encodeUDPError(e ICMPError) []byte {
	return e.append([]byte{replyError})
}

func decodeUDPReply(frame []byte) (UDPReply, error) {
	if len(frame) < 1 {
		return UDPReply{}, fmt.Errorf("mux: empty udp reply frame")
	}
	switch frame[0] {
	case replyData:
		return UDPReply{Payload: frame[1:]}, nil
	case replyError:
		e, err := decodeICMPError(frame[1:])
		if err != nil {
			return UDPReply{}, err
		}
		return UDPReply{Error: &e}, nil
	default:
		return UDPReply{}, fmt.Errorf("mux: unknown udp reply kind %d", frame[0])
	}
}

// An echo request frame is the sequence, the TTL, and the payload. The
// identifier is in the port field of the destination, so one stream serves
// one identifier.
func encodeEchoRequest(seq uint16, ttl uint8, payload []byte) []byte {
	buf := make([]byte, echoRequestHeader+len(payload))
	binary.BigEndian.PutUint16(buf[0:2], seq)
	buf[2] = ttl
	copy(buf[echoRequestHeader:], payload)
	return buf
}

func decodeEchoRequest(frame []byte) (seq uint16, ttl uint8, payload []byte, err error) {
	if len(frame) < echoRequestHeader {
		return 0, 0, nil, fmt.Errorf("mux: echo request frame too short: %d bytes", len(frame))
	}
	return binary.BigEndian.Uint16(frame[0:2]), frame[2], frame[echoRequestHeader:], nil
}

// An echo data reply is the sequence, the round-trip time in microseconds,
// and the payload.
func encodeEchoData(seq uint16, rtt time.Duration, payload []byte) []byte {
	buf := make([]byte, 1+echoDataHeader+len(payload))
	buf[0] = replyData
	binary.BigEndian.PutUint16(buf[1:3], seq)
	binary.BigEndian.PutUint32(buf[3:7], uint32(min(rtt.Microseconds(), 1<<32-1)))
	copy(buf[1+echoDataHeader:], payload)
	return buf
}

func encodeEchoError(e ICMPError) []byte {
	return e.append([]byte{replyError})
}

func decodeEchoReply(frame []byte) (EchoReply, error) {
	if len(frame) < 1 {
		return EchoReply{}, fmt.Errorf("mux: empty echo reply frame")
	}
	switch frame[0] {
	case replyData:
		body := frame[1:]
		if len(body) < echoDataHeader {
			return EchoReply{}, fmt.Errorf("mux: echo reply frame too short: %d bytes", len(frame))
		}
		return EchoReply{
			Seq:     binary.BigEndian.Uint16(body[0:2]),
			RTT:     time.Duration(binary.BigEndian.Uint32(body[2:6])) * time.Microsecond,
			Payload: body[echoDataHeader:],
		}, nil
	case replyError:
		e, err := decodeICMPError(frame[1:])
		if err != nil {
			return EchoReply{}, err
		}
		return EchoReply{Error: &e}, nil
	default:
		return EchoReply{}, fmt.Errorf("mux: unknown echo reply kind %d", frame[0])
	}
}

// append writes the error: type, code, info, 1 byte address length, the
// address, and the quoted inner bytes.
func (e ICMPError) append(buf []byte) []byte {
	buf = append(buf, e.Type, e.Code)
	buf = binary.BigEndian.AppendUint32(buf, e.Info)
	raw := e.From.Unmap().AsSlice()
	buf = append(buf, byte(len(raw)))
	buf = append(buf, raw...)
	return append(buf, e.Inner...)
}

func decodeICMPError(b []byte) (ICMPError, error) {
	if len(b) < icmpErrorHeader {
		return ICMPError{}, fmt.Errorf("mux: icmp error frame too short: %d bytes", len(b))
	}
	e := ICMPError{Type: b[0], Code: b[1], Info: binary.BigEndian.Uint32(b[2:6])}
	n := int(b[6])
	if (n != 4 && n != 16) || len(b) < icmpErrorHeader+n {
		return ICMPError{}, fmt.Errorf("mux: icmp error frame: invalid address length %d", n)
	}
	addr, _ := netip.AddrFromSlice(b[icmpErrorHeader : icmpErrorHeader+n])
	e.From = addr
	e.Inner = b[icmpErrorHeader+n:]
	return e, nil
}
