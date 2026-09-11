//go:build darwin

package darwin

import (
	"net/netip"
	"testing"
)

func TestRouteArgs(t *testing.T) {
	v4 := netip.MustParsePrefix("172.16.0.0/12")
	got := routeArgs("add", "utun7", v4)
	want := []string{"-q", "-n", "add", "-inet", "172.16.0.0/12", "-interface", "utun7"}
	assertStringSlice(t, got, want)

	v6 := netip.MustParsePrefix("2001:db8::/56")
	got = routeArgs("delete", "utun7", v6)
	want = []string{"-q", "-n", "delete", "-inet6", "2001:db8::/56", "-interface", "utun7"}
	assertStringSlice(t, got, want)
}

func TestHexMaskBits(t *testing.T) {
	cases := map[string]int{
		"0xffffffff": 32,
		"0xffffff00": 24,
		"0xff000000": 8,
		"ffffff00":   24,
	}
	for hex, want := range cases {
		got, err := hexMaskBits(hex)
		if err != nil {
			t.Fatalf("hexMaskBits(%q): %v", hex, err)
		}
		if got != want {
			t.Errorf("hexMaskBits(%q) = %d, want %d", hex, got, want)
		}
	}
	if _, err := hexMaskBits("not-hex"); err == nil {
		t.Error("hexMaskBits(\"not-hex\") should error")
	}
}

const ifconfigFixture = `lo0: flags=8049<UP,LOOPBACK,RUNNING,MULTICAST> mtu 16384
	options=1203<RXCSUM,TXCSUM,TXSTATUS,SW_TIMESTAMP>
	inet 127.0.0.1 netmask 0xff000000
	inet6 ::1 prefixlen 128
	inet6 fe80::1%lo0 prefixlen 64 scopeid 0x1
	nd6 options=201<PERFORMNUD,DAD>
en0: flags=8863<UP,BROADCAST,SMART,RUNNING,SIMPLEX,MULTICAST> mtu 1500
	options=6467<RXCSUM,TXCSUM,TSO4,TSO6,CHANNEL_IO,PARTIAL_CSUM,ZEROINVERT_CSUM>
	ether ac:de:48:00:11:22
	inet6 fe80::1234:5678:9abc:def0%en0 prefixlen 64 secured scopeid 0x6
	inet 192.168.1.5 netmask 0xffffff00 broadcast 192.168.1.255
	nd6 options=201<PERFORMNUD,DAD>
	media: autoselect
	status: active
utun7: flags=8051<UP,POINTOPOINT,RUNNING,MULTICAST> mtu 1500
	inet 169.254.117.1 --> 169.254.117.1 netmask 0xffffffff
	inet6 fd00:117::1 --> fd00:117::1 prefixlen 128
	inet6 fe80::1%utun7 prefixlen 64 scopeid 0xa
`

func TestParseConnected(t *testing.T) {
	got, err := parseConnected(ifconfigFixture)
	if err != nil {
		t.Fatalf("parseConnected: %v", err)
	}
	want := []netip.Prefix{
		netip.MustParsePrefix("fe80::/64"),
		netip.MustParsePrefix("192.168.1.0/24"),
		netip.MustParsePrefix("fe80::/64"),
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("prefix %d: got %s, want %s", i, got[i], want[i])
		}
	}
}
