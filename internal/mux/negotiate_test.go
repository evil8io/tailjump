package mux

import (
	"errors"
	"net/netip"
	"strings"
	"testing"

	"github.com/evil8io/tailjump/internal/transport"
)

const testFP = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestQUICRequestRoundTrip(t *testing.T) {
	want := QUICRequest{
		BindAddr:    netip.MustParseAddr("100.64.0.10"),
		Ports:       transport.PortRange{First: 7443, Last: 7452},
		Controller:  transport.Controller{Name: transport.Brutal, Bps: 6250000},
		Fingerprint: testFP,
	}
	line := string(want.encode())
	if line != "quic 100.64.0.10 7443-7452 brutal=6250000 "+testFP+"\n" {
		t.Fatalf("encode = %q", line)
	}
	fields := strings.Fields(strings.TrimSpace(line))
	if fields[0] != verbQUIC {
		t.Fatalf("verb = %q", fields[0])
	}
	got, err := parseQUICRequest(fields[1:])
	if err != nil {
		t.Fatalf("parseQUICRequest: %v", err)
	}
	if got != want {
		t.Fatalf("round trip = %+v, want %+v", got, want)
	}
}

func TestParseQUICRequestErrors(t *testing.T) {
	bad := [][]string{
		{},
		{"100.64.0.10", "7443-7452", "bbr"},
		{"gw.example", "7443-7452", "bbr", testFP},
		{"100.64.0.10", "7452-7443", "bbr", testFP},
		{"100.64.0.10", "7443-7452", "cubic", testFP},
		{"100.64.0.10", "7443-7452", "bbr", "sha256:short"},
		{"100.64.0.10", "7443-7452", "bbr", "md5:" + testFP[7:]},
	}
	for _, fields := range bad {
		if _, err := parseQUICRequest(fields); err == nil {
			t.Fatalf("parseQUICRequest(%q) returned no error", fields)
		}
	}
}

func TestQUICReplyRoundTrip(t *testing.T) {
	want := QUICReply{Port: 7443, Fingerprint: testFP}
	line := string(want.encode())
	if line != "quic 7443 "+testFP+"\n" {
		t.Fatalf("encode = %q", line)
	}
	got, err := parseQUICReply(line)
	if err != nil {
		t.Fatalf("parseQUICReply: %v", err)
	}
	if got != want {
		t.Fatalf("round trip = %+v, want %+v", got, want)
	}
}

func TestParseQUICReplyUnavailable(t *testing.T) {
	line := string(encodeUnavailable("no free udp port in 7443-7452 on 100.64.0.10: address in use"))
	_, err := parseQUICReply(line)
	var ue *UnavailableError
	if !errors.As(err, &ue) {
		t.Fatalf("parseQUICReply(%q) = %v, want *UnavailableError", line, err)
	}
	if ue.Reason != "no free udp port in 7443-7452 on 100.64.0.10: address in use" {
		t.Fatalf("reason = %q", ue.Reason)
	}
	for _, bad := range []string{"", "ok 7443 " + testFP, "quic 0 " + testFP, "quic 7443", "quic 7443 x"} {
		if _, err := parseQUICReply(bad); err == nil {
			t.Fatalf("parseQUICReply(%q) returned no error", bad)
		}
	}
}
