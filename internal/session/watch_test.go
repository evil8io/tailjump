package session

import (
	"net/netip"
	"testing"

	"github.com/evil8io/tailjump/internal/tailnet"
)

func TestEndpointInside(t *testing.T) {
	remote := netip.MustParseAddr("100.64.0.10")
	networks := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/16"), netip.MustParsePrefix("2001:db8::/56")}
	peer := func(cur string) *tailnet.Status {
		return &tailnet.Status{Peers: []tailnet.Peer{{HostName: "gw", TailscaleIPs: []netip.Addr{remote}, CurAddr: cur}}}
	}
	cases := []struct {
		cur    string
		inside bool
	}{
		{"10.0.0.10:41641", true},
		{"[2001:db8:0:1::a]:41641", true},
		{"203.0.113.5:41641", false},
		{"", false},
	}
	for _, c := range cases {
		if _, got := endpointInside(peer(c.cur), remote, networks); got != c.inside {
			t.Errorf("endpointInside(%q) = %v, want %v", c.cur, got, c.inside)
		}
	}
	other := &tailnet.Status{Peers: []tailnet.Peer{{HostName: "x", TailscaleIPs: []netip.Addr{netip.MustParseAddr("100.64.0.11")}, CurAddr: "10.0.0.10:41641"}}}
	if _, got := endpointInside(other, remote, networks); got {
		t.Error("a different peer counted as the remote")
	}
}
