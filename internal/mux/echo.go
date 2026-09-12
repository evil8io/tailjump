package mux

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrEchoUnsupported is the client's error when the helper has no socket
// for ICMP echo: neither a raw ICMP socket nor a ping socket opened.
var ErrEchoUnsupported = errors.New("the remote has no socket for icmp echo")

// EchoConn is an ICMP echo flow over one stream, to one destination and one
// identifier. Request frames go to the helper; reply frames come back.
type EchoConn struct {
	stream Stream
}

// WriteRequest sends one echo request with the TTL.
func (e *EchoConn) WriteRequest(seq uint16, ttl uint8, payload []byte) error {
	return writeFrame(e.stream, encodeEchoRequest(seq, ttl, payload))
}

// ReadStatus reads the helper's status byte, which precedes the reply
// frames. It returns ErrEchoUnsupported when the helper has no echo socket,
// and the dial error for another non-zero status.
func (e *EchoConn) ReadStatus() error {
	status, err := readStatus(e.stream)
	if err != nil {
		return err
	}
	switch status {
	case statusOK:
		return nil
	case statusUnsupported:
		return ErrEchoUnsupported
	default:
		return statusError(status)
	}
}

// ReadReply reads one reply frame. It returns an error when the helper
// closes the flow, for example on its idle timeout.
func (e *EchoConn) ReadReply() (EchoReply, error) {
	frame, err := readFrame(e.stream)
	if err != nil {
		return EchoReply{}, err
	}
	return decodeEchoReply(frame)
}

// SetReadDeadline sets the deadline for ReadStatus and ReadReply.
func (e *EchoConn) SetReadDeadline(t time.Time) error {
	return e.stream.SetReadDeadline(t)
}

// Close ends the flow.
func (e *EchoConn) Close() error {
	return e.stream.Close()
}

// Echo socket kinds, the helper's answer to the icmp control verb.
const (
	EchoSocketRaw  = "raw"
	EchoSocketPing = "ping"
	EchoSocketNone = "none"
)

// EchoSocketInfo is the helper's answer to the icmp control verb: which
// socket the remote offers for ICMP echo, and the value of the sysctl
// net.ipv4.ping_group_range, which gates the ping socket.
type EchoSocketInfo struct {
	Socket         string
	PingGroupRange string
}

// String formats the info for tj doctor.
func (i EchoSocketInfo) String() string {
	rng := ""
	if i.PingGroupRange != "" {
		rng = fmt.Sprintf(" (ping_group_range %s)", strings.ReplaceAll(i.PingGroupRange, "-", " "))
	}
	switch i.Socket {
	case EchoSocketRaw:
		return "raw socket"
	case EchoSocketPing:
		return "ping socket" + rng
	default:
		return "none" + rng
	}
}

func (i EchoSocketInfo) encode() []byte {
	if i.PingGroupRange == "" {
		return fmt.Appendf(nil, "%s %s\n", verbICMP, i.Socket)
	}
	return fmt.Appendf(nil, "%s %s %s\n", verbICMP, i.Socket, i.PingGroupRange)
}

func parseEchoSocketInfo(line string) (EchoSocketInfo, error) {
	fields := strings.Fields(line)
	if len(fields) < 2 || len(fields) > 3 || fields[0] != verbICMP {
		return EchoSocketInfo{}, fmt.Errorf("icmp reply: unexpected line %q", line)
	}
	switch fields[1] {
	case EchoSocketRaw, EchoSocketPing, EchoSocketNone:
	default:
		return EchoSocketInfo{}, fmt.Errorf("icmp reply: unknown socket %q", fields[1])
	}
	info := EchoSocketInfo{Socket: fields[1]}
	if len(fields) == 3 {
		info.PingGroupRange = fields[2]
	}
	return info, nil
}
