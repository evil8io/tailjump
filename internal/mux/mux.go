// Package mux is the client side and the helper side of the tj mux protocol.
// The SSH transport is the helper's stdin and stdout. The helper writes the
// line TJ4 at start, then both sides run yamux over the transport. The client
// opens every stream: a control stream, one stream per TCP connection, one
// stream per UDP flow, and one stream per ICMP echo flow. The control stream
// negotiates the QUIC transport, and the flows then run as QUIC streams with
// the same wire format. This package imports yamux, the quic-go fork,
// x/sys/unix, and the standard library only, so the helper that embeds it
// stays small.
package mux

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/hashicorp/yamux"
)

// Stream kinds, the first byte of every stream the client opens.
const (
	kindControl byte = 0
	kindTCP     byte = 1
	kindUDP     byte = 2
	kindProbe   byte = 3
	kindICMP    byte = 4
)

// Dial status, the helper's reply on a TCP or UDP stream.
const (
	statusOK          byte = 0
	statusRefused     byte = 1
	statusUnreachable byte = 2
	statusTimeout     byte = 3
	statusOther       byte = 4
	statusUnsupported byte = 5
)

// handshakeLine is the line the helper writes to the transport at start.
const handshakeLine = "TJ4"

const (
	handshakeTimeout = 10 * time.Second
	dialTimeout      = 10 * time.Second

	keepAliveInterval = 5 * time.Second
	connWriteTimeout  = 20 * time.Second
	maxStreamWindow   = 4 << 20

	udpIdle    = 60 * time.Second
	udpIdleDNS = 10 * time.Second
	dnsPort    = 53

	maxLineLen  = 4096
	maxUDPFrame = 0xffff

	quicALPN             = "tj/4"
	quicHandshakeTimeout = 5 * time.Second
	quicReplyTimeout     = 15 * time.Second
	quicIdleTimeout      = 15 * time.Second
	quicKeepAlive        = 5 * time.Second
	quicPacketSize       = 1232
	quicMaxStreams       = 1 << 16
	quicOpenTimeout      = 10 * time.Second
	quicStreamWindow     = 8 << 20
	quicConnWindow       = 20 << 20
)

// Control stream verbs.
const (
	verbQuit        = "quit"
	verbQUIC        = "quic"
	verbUnavailable = "quic-unavailable"
	verbAbandon     = "quic-abandon"
	verbICMP        = "icmp"
	verbUnlink      = "unlink"
)

// Errors a client Dial returns for a non-zero helper status.
var (
	ErrRefused     = errors.New("connection refused")
	ErrUnreachable = errors.New("network unreachable")
	ErrDialTimeout = errors.New("dial timeout")
	ErrDialFailed  = errors.New("dial failed")
)

// Stream is one bidirectional flow of the mux transport. A yamux stream
// satisfies it, and a QUIC stream will satisfy it too.
type Stream interface {
	io.Reader
	io.Writer
	io.Closer
	SetReadDeadline(t time.Time) error
}

// Dialer opens TCP, UDP, and ICMP echo flows to the remote helper. The
// yamux Client and the QUICClient implement it.
type Dialer interface {
	DialTCP(dst netip.AddrPort) (net.Conn, error)
	DialUDP(dst netip.AddrPort) (*UDPConn, error)
	DialICMP(dst netip.Addr, ident uint16) (*EchoConn, error)
}

// ControlInfo is the JSON line the helper writes on the control stream.
type ControlInfo struct {
	Version  string `json:"version"`
	GOOS     string `json:"goos"`
	GOARCH   string `json:"goarch"`
	Hostname string `json:"hostname"`
	PID      int    `json:"pid"`
}

// encode builds the control line with fmt, not encoding/json, so the helper
// binary does not link encoding/json. The values are ASCII, so %q produces
// valid JSON strings.
func (ci ControlInfo) encode() []byte {
	return fmt.Appendf(nil, `{"version":%q,"goos":%q,"goarch":%q,"hostname":%q,"pid":%d}`+"\n",
		ci.Version, ci.GOOS, ci.GOARCH, ci.Hostname, ci.PID)
}

// decodeControlInfo parses the line that encode emits. It uses strconv and
// strings, not encoding/json, because the client shares this package with the
// helper and the helper must link no encoding/json. The %q strings that encode
// writes are Go-quoted, which strconv.Unquote reverses exactly.
func decodeControlInfo(line string) (ControlInfo, error) {
	var ci ControlInfo
	line = strings.TrimSpace(line)
	if len(line) < 2 || line[0] != '{' || line[len(line)-1] != '}' {
		return ci, fmt.Errorf("mux: malformed control line")
	}
	for _, field := range splitTopLevel(line[1:len(line)-1], ',') {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		sep := indexTopLevel(field, ':')
		if sep < 0 {
			return ci, fmt.Errorf("mux: malformed control field %q", field)
		}
		key, err := strconv.Unquote(strings.TrimSpace(field[:sep]))
		if err != nil {
			return ci, fmt.Errorf("mux: control key: %w", err)
		}
		raw := strings.TrimSpace(field[sep+1:])
		switch key {
		case "version":
			ci.Version, err = strconv.Unquote(raw)
		case "goos":
			ci.GOOS, err = strconv.Unquote(raw)
		case "goarch":
			ci.GOARCH, err = strconv.Unquote(raw)
		case "hostname":
			ci.Hostname, err = strconv.Unquote(raw)
		case "pid":
			ci.PID, err = strconv.Atoi(raw)
		}
		if err != nil {
			return ci, fmt.Errorf("mux: control field %q: %w", key, err)
		}
	}
	return ci, nil
}

// indexTopLevel returns the index of the first sep byte outside a double-quoted
// string, or -1.
func indexTopLevel(s string, sep byte) int {
	inStr := false
	esc := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case esc:
			esc = false
		case inStr && c == '\\':
			esc = true
		case c == '"':
			inStr = !inStr
		case !inStr && c == sep:
			return i
		}
	}
	return -1
}

// splitTopLevel splits s at every sep byte outside a double-quoted string.
func splitTopLevel(s string, sep byte) []string {
	var parts []string
	start := 0
	inStr := false
	esc := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case esc:
			esc = false
		case inStr && c == '\\':
			esc = true
		case c == '"':
			inStr = !inStr
		case !inStr && c == sep:
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	return append(parts, s[start:])
}

func muxConfig() *yamux.Config {
	c := yamux.DefaultConfig()
	c.EnableKeepAlive = true
	c.KeepAliveInterval = keepAliveInterval
	c.ConnectionWriteTimeout = connWriteTimeout
	c.MaxStreamWindowSize = maxStreamWindow
	c.LogOutput = io.Discard
	return c
}

// idleFor returns the UDP idle timeout for a destination port. Queries to
// port 53 close faster because a resolver flow is short.
func idleFor(port uint16, def, dns time.Duration) time.Duration {
	if port == dnsPort {
		return dns
	}
	return def
}

func statusError(status byte) error {
	switch status {
	case statusRefused:
		return ErrRefused
	case statusUnreachable:
		return ErrUnreachable
	case statusTimeout:
		return ErrDialTimeout
	default:
		return ErrDialFailed
	}
}

// statusFor maps a dial error to a status byte.
func statusFor(err error) byte {
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return statusTimeout
	}
	switch {
	case errors.Is(err, syscall.ECONNREFUSED):
		return statusRefused
	case errors.Is(err, syscall.ENETUNREACH), errors.Is(err, syscall.EHOSTUNREACH):
		return statusUnreachable
	case errors.Is(err, context.DeadlineExceeded):
		return statusTimeout
	default:
		return statusOther
	}
}

// writeDest writes the destination: 1 byte address length, the address bytes,
// and 2 bytes port big-endian.
func writeDest(w io.Writer, dst netip.AddrPort) error {
	addr := dst.Addr().Unmap()
	var raw []byte
	if addr.Is4() {
		b := addr.As4()
		raw = b[:]
	} else {
		b := addr.As16()
		raw = b[:]
	}
	buf := make([]byte, 0, 1+len(raw)+2)
	buf = append(buf, byte(len(raw)))
	buf = append(buf, raw...)
	buf = binary.BigEndian.AppendUint16(buf, dst.Port())
	_, err := w.Write(buf)
	return err
}

func readDest(r io.Reader) (netip.AddrPort, error) {
	var length [1]byte
	if _, err := io.ReadFull(r, length[:]); err != nil {
		return netip.AddrPort{}, err
	}
	var addr netip.Addr
	switch length[0] {
	case 4:
		var b [4]byte
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return netip.AddrPort{}, err
		}
		addr = netip.AddrFrom4(b)
	case 16:
		var b [16]byte
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return netip.AddrPort{}, err
		}
		addr = netip.AddrFrom16(b)
	default:
		return netip.AddrPort{}, fmt.Errorf("mux: invalid address length %d", length[0])
	}
	var port [2]byte
	if _, err := io.ReadFull(r, port[:]); err != nil {
		return netip.AddrPort{}, err
	}
	return netip.AddrPortFrom(addr, binary.BigEndian.Uint16(port[:])), nil
}

// writeFrame writes a flow frame: 2 bytes length big-endian, then the body.
func writeFrame(w io.Writer, p []byte) error {
	if len(p) > maxUDPFrame {
		return fmt.Errorf("mux: udp frame too large: %d", len(p))
	}
	buf := make([]byte, 2+len(p))
	binary.BigEndian.PutUint16(buf[:2], uint16(len(p)))
	copy(buf[2:], p)
	_, err := w.Write(buf)
	return err
}

func readFrame(r io.Reader) ([]byte, error) {
	var hdr [2]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint16(hdr[:])
	if n == 0 {
		return []byte{}, nil
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

func readStatus(r io.Reader) (byte, error) {
	var b [1]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return 0, err
	}
	return b[0], nil
}

// readLine reads one line ending in a newline. It reads a byte at a time so it
// consumes nothing past the newline, which matters before yamux takes over the
// transport. It drops a trailing carriage return.
func readLine(r io.Reader, limit int) (string, error) {
	buf := make([]byte, 0, 16)
	var b [1]byte
	for {
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return "", err
		}
		if b[0] == '\n' {
			if n := len(buf); n > 0 && buf[n-1] == '\r' {
				buf = buf[:n-1]
			}
			return string(buf), nil
		}
		if len(buf) >= limit {
			return "", fmt.Errorf("mux: line exceeds %d bytes", limit)
		}
		buf = append(buf, b[0])
	}
}
