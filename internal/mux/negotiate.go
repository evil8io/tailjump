package mux

import (
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"

	"github.com/evil8io/tailjump/internal/transport"
)

// QUICRequest is the client's quic line on the control stream: the address
// the helper binds, the port range, the helper's congestion controller, and
// the fingerprint of the client certificate.
type QUICRequest struct {
	BindAddr    netip.Addr
	Ports       transport.PortRange
	Controller  transport.Controller
	Fingerprint string
}

// QUICReply is the helper's answer: the bound port and the fingerprint of the
// helper certificate.
type QUICReply struct {
	Port        uint16
	Fingerprint string
}

// UnavailableError is the helper's quic-unavailable answer.
type UnavailableError struct {
	Reason string
}

func (e *UnavailableError) Error() string {
	return "quic transport unavailable: " + e.Reason
}

func (r QUICRequest) encode() []byte {
	return fmt.Appendf(nil, "%s %s %s %s %s\n",
		verbQUIC, r.BindAddr, r.Ports, r.Controller, r.Fingerprint)
}

// parseQUICRequest parses the fields after the quic verb.
func parseQUICRequest(fields []string) (QUICRequest, error) {
	if len(fields) != 4 {
		return QUICRequest{}, fmt.Errorf("quic request: want 4 fields, got %d", len(fields))
	}
	addr, err := netip.ParseAddr(fields[0])
	if err != nil {
		return QUICRequest{}, fmt.Errorf("quic request: bind address: %w", err)
	}
	ports, err := transport.ParsePortRange(fields[1])
	if err != nil {
		return QUICRequest{}, fmt.Errorf("quic request: %w", err)
	}
	ctl, err := transport.ParseController(fields[2])
	if err != nil {
		return QUICRequest{}, fmt.Errorf("quic request: %w", err)
	}
	if err := checkFingerprint(fields[3]); err != nil {
		return QUICRequest{}, fmt.Errorf("quic request: %w", err)
	}
	return QUICRequest{BindAddr: addr, Ports: ports, Controller: ctl, Fingerprint: fields[3]}, nil
}

func (r QUICReply) encode() []byte {
	return fmt.Appendf(nil, "%s %d %s\n", verbQUIC, r.Port, r.Fingerprint)
}

func encodeUnavailable(reason string) []byte {
	return fmt.Appendf(nil, "%s %s\n", verbUnavailable, strings.ReplaceAll(reason, "\n", " "))
}

// parseQUICReply parses the helper's answer line. An unavailable answer
// returns an *UnavailableError.
func parseQUICReply(line string) (QUICReply, error) {
	verb, rest, _ := strings.Cut(strings.TrimSpace(line), " ")
	switch verb {
	case verbUnavailable:
		return QUICReply{}, &UnavailableError{Reason: strings.TrimSpace(rest)}
	case verbQUIC:
		fields := strings.Fields(rest)
		if len(fields) != 2 {
			return QUICReply{}, fmt.Errorf("quic reply: want 2 fields, got %d", len(fields))
		}
		port, err := strconv.ParseUint(fields[0], 10, 16)
		if err != nil || port == 0 {
			return QUICReply{}, fmt.Errorf("quic reply: invalid port %q", fields[0])
		}
		if err := checkFingerprint(fields[1]); err != nil {
			return QUICReply{}, fmt.Errorf("quic reply: %w", err)
		}
		return QUICReply{Port: uint16(port), Fingerprint: fields[1]}, nil
	default:
		return QUICReply{}, fmt.Errorf("quic reply: unexpected line %q", line)
	}
}

func checkFingerprint(s string) error {
	hexPart, ok := strings.CutPrefix(s, fingerprintPrefix)
	if !ok || len(hexPart) != 64 {
		return errors.New("fingerprint: want sha256:<64 hex digits>")
	}
	for _, c := range hexPart {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return errors.New("fingerprint: want lowercase hex digits")
		}
	}
	return nil
}
