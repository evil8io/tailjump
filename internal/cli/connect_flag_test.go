package cli

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/evil8io/tailjump/internal/dns"
	"github.com/evil8io/tailjump/internal/protocols"
)

// TestConnectNetworkAndExcludeFlags locks in that connect can both add a
// route with --network and drop one with --exclude, and that both repeat.
func TestConnectNetworkAndExcludeFlags(t *testing.T) {
	cmd := newConnectCmd()
	for _, name := range []string{"network", "exclude"} {
		f := cmd.Flags().Lookup(name)
		if f == nil {
			t.Fatalf("connect is missing --%s", name)
		}
		if f.Value.Type() != "stringArray" {
			t.Errorf("--%s is %s, want stringArray so it repeats", name, f.Value.Type())
		}
	}
}

// TestConnectProtocolsFlag locks in the --protocols flag on connect and on
// the remote alias commands.
func TestConnectProtocolsFlag(t *testing.T) {
	for _, cmd := range []*cobra.Command{newConnectCmd(), newRemoteAddCmd(), newRemoteSetCmd()} {
		if cmd.Flags().Lookup("protocols") == nil {
			t.Fatalf("%s is missing --protocols", cmd.Name())
		}
	}
}

// TestCheckDNSProtocols locks in the rule: split and all send the queries
// through the tunnel, so they need udp in the set; none needs nothing.
func TestCheckDNSProtocols(t *testing.T) {
	cases := []struct {
		mode dns.Mode
		set  string
		ok   bool
	}{
		{dns.ModeNone, "tcp", true},
		{dns.ModeNone, "tcp,udp,icmp", true},
		{dns.ModeSplit, "tcp,udp", true},
		{dns.ModeAll, "udp", true},
		{dns.ModeSplit, "tcp", false},
		{dns.ModeAll, "tcp,icmp", false},
	}
	for _, c := range cases {
		set, err := protocols.Parse(c.set)
		if err != nil {
			t.Fatal(err)
		}
		err = checkDNSProtocols(c.mode, set)
		if (err == nil) != c.ok {
			t.Fatalf("checkDNSProtocols(%s, %s) = %v, want ok=%v", c.mode, c.set, err, c.ok)
		}
		if err != nil && !strings.Contains(err.Error(), "udp") {
			t.Fatalf("error %q does not name udp", err)
		}
	}
}
