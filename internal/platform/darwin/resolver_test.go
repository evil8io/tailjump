//go:build darwin

package darwin

import (
	"net/netip"
	"os"
	"path/filepath"
	"testing"
)

func withTempRuntimeDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	old := runtimeDir
	runtimeDir = dir
	t.Cleanup(func() { runtimeDir = old })
	return dir
}

func withTempResolverDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	old := resolverDir
	resolverDir = dir
	t.Cleanup(func() { resolverDir = old })
	return dir
}

func TestParseRouteGetInterface(t *testing.T) {
	out := "   route to: default\ndestination: default\n       mask: default\n    gateway: 192.168.1.1\n  interface: en0\n      flags: <UP,GATEWAY,DONE,STATIC,PRCLONING>\n"
	got, err := parseRouteGetInterface(out)
	if err != nil {
		t.Fatalf("parseRouteGetInterface: %v", err)
	}
	if got != "en0" {
		t.Errorf("got %q, want en0", got)
	}

	if _, err := parseRouteGetInterface("no interface line here"); err == nil {
		t.Error("expected an error when no interface line is present")
	}
}

const hardwarePortsFixture = `Hardware Port: Wi-Fi
Device: en0
Ethernet Address: ac:de:48:00:11:22

Hardware Port: Thunderbolt Ethernet
Device: en5
Ethernet Address: N/A

VLAN Configurations
===================
`

func TestParseHardwarePorts(t *testing.T) {
	got := parseHardwarePorts(hardwarePortsFixture)
	if got["en0"] != "Wi-Fi" {
		t.Errorf("en0 = %q, want Wi-Fi", got["en0"])
	}
	if got["en5"] != "Thunderbolt Ethernet" {
		t.Errorf("en5 = %q, want Thunderbolt Ethernet", got["en5"])
	}
}

func TestParseDNSServersOutput(t *testing.T) {
	got, err := parseDNSServersOutput("10.1.0.2\n10.1.0.3\n")
	if err != nil {
		t.Fatalf("parseDNSServersOutput: %v", err)
	}
	want := []netip.Addr{netip.MustParseAddr("10.1.0.2"), netip.MustParseAddr("10.1.0.3")}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("got %v, want %v", got, want)
	}

	empty, err := parseDNSServersOutput("There aren't any DNS Servers set on Wi-Fi.\n")
	if err != nil {
		t.Fatalf("parseDNSServersOutput: %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("got %v, want none", empty)
	}
}

func TestParseSearchDomainsOutput(t *testing.T) {
	got := parseSearchDomainsOutput("corp.example\ninternal.example\n")
	want := []string{"corp.example", "internal.example"}
	assertStringSlice(t, got, want)

	none := parseSearchDomainsOutput("There aren't any Search Domains set on Wi-Fi.\n")
	if len(none) != 0 {
		t.Errorf("got %v, want none", none)
	}
}

func TestSplitApplyAndRevert(t *testing.T) {
	withTempRuntimeDir(t)
	withTempResolverDir(t)

	r := &Resolver{}
	servers := []netip.Addr{netip.MustParseAddr("10.1.0.2")}
	domains := []string{"corp.example", "internal.example"}
	if err := r.ApplySplit("utun7", servers, domains); err != nil {
		t.Fatalf("ApplySplit: %v", err)
	}

	for _, d := range domains {
		path := filepath.Join(resolverDir, d)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if string(data) != "nameserver 10.1.0.2\n" {
			t.Errorf("content of %s = %q", path, data)
		}
	}

	if err := r.Revert("utun7"); err != nil {
		t.Fatalf("Revert: %v", err)
	}
	for _, d := range domains {
		path := filepath.Join(resolverDir, d)
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("%s still exists after Revert", path)
		}
	}
	if _, err := os.Stat(splitStatePath("utun7")); !os.IsNotExist(err) {
		t.Error("split state file still exists after Revert")
	}

	// A second Revert on an already-reverted device is a no-op.
	if err := r.Revert("utun7"); err != nil {
		t.Fatalf("second Revert: %v", err)
	}
}

func TestAllStateRoundTrip(t *testing.T) {
	withTempRuntimeDir(t)

	oldDNS := []netip.Addr{netip.MustParseAddr("192.168.1.1")}
	oldSearch := []string{"lan"}
	if err := saveAllState("utun7", "Wi-Fi", oldDNS, oldSearch); err != nil {
		t.Fatalf("saveAllState: %v", err)
	}

	st, err := loadAllState("utun7")
	if err != nil {
		t.Fatalf("loadAllState: %v", err)
	}
	if st == nil {
		t.Fatal("loadAllState returned nil")
	}
	if st.service != "Wi-Fi" {
		t.Errorf("service = %q, want Wi-Fi", st.service)
	}
	assertStringSlice(t, st.dns, []string{"192.168.1.1"})
	assertStringSlice(t, st.search, []string{"lan"})

	if err := os.Remove(allStatePath("utun7")); err != nil {
		t.Fatalf("remove state: %v", err)
	}
	st, err = loadAllState("utun7")
	if err != nil {
		t.Fatalf("loadAllState after removal: %v", err)
	}
	if st != nil {
		t.Errorf("loadAllState after removal = %+v, want nil", st)
	}
}
