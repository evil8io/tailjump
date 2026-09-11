package manifest

import (
	"net/netip"
	"testing"
)

func TestParseRejectsWrongVersion(t *testing.T) {
	if _, err := Parse([]byte("version: 2\nname: x\n")); err == nil {
		t.Fatal("want error for version 2")
	}
}

func TestParseDiscoveryDefaults(t *testing.T) {
	m, err := Parse([]byte("version: 1\nname: x\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !m.LinkRoutesEnabled() || !m.CloudEnabled() {
		t.Fatal("absent discovery section must mean all sources on")
	}
	off := false
	m.Discovery = &Discovery{LinkRoutes: &off}
	if m.LinkRoutesEnabled() {
		t.Fatal("link_routes false must disable")
	}
	if !m.CloudEnabled() {
		t.Fatal("cloud stays on when only link_routes is set")
	}
}

func TestComputeNetworksExcludesTailnetRemoteAndLaptop(t *testing.T) {
	mustP := netip.MustParsePrefix
	in := Inputs{
		ManifestNetworks: []netip.Prefix{mustP("10.50.0.0/16")},
		DiscoveryCloud:   []netip.Prefix{mustP("100.64.0.0/16")}, // inside tailnet, must drop
		RemoteAddrs:      []netip.Addr{netip.MustParseAddr("10.50.6.26")},
		LaptopConnected:  []netip.Prefix{mustP("192.168.240.0/24")},
	}
	got, err := ComputeNetworks(in)
	if err != nil {
		t.Fatal(err)
	}
	// 100.64.0.0/16 dropped (tailnet); 10.50.6.26 carved out of 10.50.0.0/16.
	var has10, hasTailnet bool
	for _, p := range got {
		if p.Overlaps(mustP("100.64.0.0/16")) {
			hasTailnet = true
		}
		if p.Overlaps(mustP("10.50.0.0/16")) {
			has10 = true
		}
		if p.Contains(netip.MustParseAddr("10.50.6.26")) {
			t.Fatalf("remote addr 10.50.6.26 must be excluded, in %v", p)
		}
	}
	if hasTailnet {
		t.Fatal("tailnet range must be excluded")
	}
	if !has10 {
		t.Fatal("10.50.0.0/16 must remain")
	}
}
