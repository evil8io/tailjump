package mux

import (
	"bytes"
	"net/netip"
	"testing"
	"time"
)

func TestUDPFramesRoundTrip(t *testing.T) {
	ttl, payload, err := decodeUDPRequest(encodeUDPRequest(7, []byte("dns")))
	if err != nil || ttl != 7 || string(payload) != "dns" {
		t.Fatalf("udp request = %d %q %v", ttl, payload, err)
	}
	reply, err := decodeUDPReply(encodeUDPData([]byte("answer")))
	if err != nil || reply.Error != nil || string(reply.Payload) != "answer" {
		t.Fatalf("udp data reply = %+v %v", reply, err)
	}
	want := ICMPError{Type: 11, Code: 0, Info: 0, From: netip.MustParseAddr("10.0.0.1"), Inner: []byte{1, 2, 3}}
	reply, err = decodeUDPReply(encodeUDPError(want))
	if err != nil || reply.Error == nil || reply.Payload != nil {
		t.Fatalf("udp error reply = %+v %v", reply, err)
	}
	if got := *reply.Error; got.Type != want.Type || got.Code != want.Code || got.Info != want.Info || got.From != want.From || !bytes.Equal(got.Inner, want.Inner) {
		t.Fatalf("udp error = %+v, want %+v", got, want)
	}
}

func TestEchoFramesRoundTrip(t *testing.T) {
	seq, ttl, payload, err := decodeEchoRequest(encodeEchoRequest(0xbeef, 3, []byte("hello")))
	if err != nil || seq != 0xbeef || ttl != 3 || string(payload) != "hello" {
		t.Fatalf("echo request = %d %d %q %v", seq, ttl, payload, err)
	}
	reply, err := decodeEchoReply(encodeEchoData(9, 1500*time.Microsecond, []byte("hello")))
	if err != nil || reply.Error != nil || reply.Seq != 9 || reply.RTT != 1500*time.Microsecond || string(reply.Payload) != "hello" {
		t.Fatalf("echo data reply = %+v %v", reply, err)
	}
	want := ICMPError{Type: 3, Code: 0, Info: 1280, From: netip.MustParseAddr("2001:db8::1"), Inner: []byte{128, 0, 0, 0, 0, 1, 0, 9}}
	reply, err = decodeEchoReply(encodeEchoError(want))
	if err != nil || reply.Error == nil {
		t.Fatalf("echo error reply = %+v %v", reply, err)
	}
	if got := *reply.Error; got.Type != want.Type || got.Info != want.Info || got.From != want.From || !bytes.Equal(got.Inner, want.Inner) {
		t.Fatalf("echo error = %+v, want %+v", got, want)
	}
}

func TestFramesRejectShort(t *testing.T) {
	if _, _, err := decodeUDPRequest(nil); err == nil {
		t.Fatal("empty udp request decoded")
	}
	if _, err := decodeUDPReply(nil); err == nil {
		t.Fatal("empty udp reply decoded")
	}
	if _, err := decodeUDPReply([]byte{9}); err == nil {
		t.Fatal("unknown udp reply kind decoded")
	}
	if _, _, _, err := decodeEchoRequest([]byte{0, 1}); err == nil {
		t.Fatal("short echo request decoded")
	}
	if _, err := decodeEchoReply([]byte{replyData, 0, 1}); err == nil {
		t.Fatal("short echo data reply decoded")
	}
	if _, err := decodeICMPError([]byte{3, 3, 0, 0, 0, 0, 5}); err == nil {
		t.Fatal("icmp error with an invalid address length decoded")
	}
}

func TestEchoSocketInfoLine(t *testing.T) {
	cases := []struct {
		info EchoSocketInfo
		line string
		text string
	}{
		{EchoSocketInfo{Socket: EchoSocketRaw}, "icmp raw\n", "raw socket"},
		{EchoSocketInfo{Socket: EchoSocketPing, PingGroupRange: "0-2147483647"}, "icmp ping 0-2147483647\n", "ping socket (ping_group_range 0 2147483647)"},
		{EchoSocketInfo{Socket: EchoSocketNone, PingGroupRange: "1-0"}, "icmp none 1-0\n", "none (ping_group_range 1 0)"},
	}
	for _, c := range cases {
		if got := string(c.info.encode()); got != c.line {
			t.Fatalf("encode(%+v) = %q, want %q", c.info, got, c.line)
		}
		got, err := parseEchoSocketInfo(c.line)
		if err != nil || got != c.info {
			t.Fatalf("parse(%q) = %+v %v, want %+v", c.line, got, err, c.info)
		}
		if got.String() != c.text {
			t.Fatalf("String() = %q, want %q", got.String(), c.text)
		}
	}
	if _, err := parseEchoSocketInfo("icmp tcp"); err == nil {
		t.Fatal("unknown socket parsed")
	}
}

// TestICMPChecksum checks the RFC 1071 sum against an echo request whose
// checksum is known: type 8, id 1, seq 1, no payload sums to 0xf7fd.
func TestICMPChecksum(t *testing.T) {
	msg := []byte{8, 0, 0, 0, 0, 1, 0, 1}
	if got := icmpChecksum(msg); got != 0xf7fd {
		t.Fatalf("icmpChecksum = %#x, want 0xf7fd", got)
	}
	// The checksum field itself is skipped, so a filled field gives the
	// same sum.
	msg[2], msg[3] = 0xf7, 0xfd
	if got := icmpChecksum(msg); got != 0xf7fd {
		t.Fatalf("icmpChecksum with the field set = %#x, want 0xf7fd", got)
	}
}
