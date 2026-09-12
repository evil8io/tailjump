package session

import (
	"net/netip"
	"testing"

	"github.com/evil8io/tailjump/internal/tailnet"
)

func TestPathChanges(t *testing.T) {
	cases := []struct {
		samples []string
		want    int
	}{
		{nil, 0},
		{[]string{"a"}, 0},
		{[]string{"", "", "a", "a", "a"}, 1},
		{[]string{"a", "", "a", "", "a"}, 4},
		{[]string{"", "", "", ""}, 0},
	}
	for _, c := range cases {
		if got := pathChanges(c.samples); got != c.want {
			t.Errorf("pathChanges(%v) = %d, want %d", c.samples, got, c.want)
		}
	}
}

func TestEndpointOf(t *testing.T) {
	remote := netip.MustParseAddr("100.64.0.10")
	st := &tailnet.Status{Peers: []tailnet.Peer{
		{HostName: "other", TailscaleIPs: []netip.Addr{netip.MustParseAddr("100.64.0.11")}, CurAddr: "203.0.113.5:41641"},
		{HostName: "gw", TailscaleIPs: []netip.Addr{remote}, CurAddr: "[2001:db8::a]:41641"},
	}}
	if got := endpointOf(st, remote); got != "[2001:db8::a]:41641" {
		t.Errorf("endpointOf = %q", got)
	}
	if got := endpointOf(st, netip.MustParseAddr("100.64.0.12")); got != "" {
		t.Errorf("unknown peer = %q", got)
	}
}
