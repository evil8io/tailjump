//go:build darwin

package darwin

import (
	"net/netip"
	"testing"
)

func TestNormalizeUtunName(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		{"utun", "utun"},
		{"utun0", "utun0"},
		{"utun12", "utun12"},
		{"", "utun"},
		{"tj0", "utun"},
		{"utuna", "utun"},
		{"utun12abc", "utun"},
	}
	for _, c := range cases {
		if got := normalizeUtunName(c.name); got != c.want {
			t.Errorf("normalizeUtunName(%q) = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestInet4AliasArgs(t *testing.T) {
	p := netip.MustParsePrefix("169.254.117.1/32")
	got := inet4AliasArgs("utun7", p)
	want := []string{"utun7", "inet", "169.254.117.1", "169.254.117.1", "netmask", "255.255.255.255", "alias"}
	assertStringSlice(t, got, want)
}

func TestInet6AliasArgs(t *testing.T) {
	p := netip.MustParsePrefix("fd00:117::1/128")
	got := inet6AliasArgs("utun7", p)
	want := []string{"utun7", "inet6", "fd00:117::1", "prefixlen", "128", "alias"}
	assertStringSlice(t, got, want)
}

func assertStringSlice(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}
