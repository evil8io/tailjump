package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/evil8io/tailjump/internal/config"
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
	for _, cmd := range []*cobra.Command{newConnectCmd(), newAliasAddCmd(), newAliasSetCmd()} {
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

// TestReconnectFor locks in the precedence: the flag, then
// remotes.<ref>.reconnect_for, then defaults.reconnect_for, then 10 minutes.
func TestReconnectFor(t *testing.T) {
	cfg := &config.Config{
		Defaults: config.Defaults{ReconnectFor: "5m"},
		Remotes: map[string]config.RemoteConfig{
			"gw": {Host: "gw.example", ReconnectFor: "2m"},
		},
	}
	cases := []struct {
		name string
		cfg  *config.Config
		flag string
		ref  string
		want time.Duration
	}{
		{"flag wins over remote and defaults", cfg, "1m", "gw", time.Minute},
		{"flag off wins even when remote and defaults are set", cfg, "0", "gw", 0},
		{"remote wins over defaults", cfg, "", "gw", 2 * time.Minute},
		{"defaults win with no matching remote", cfg, "", "other", 5 * time.Minute},
		{"10 minutes with nothing set", &config.Config{}, "", "gw", 10 * time.Minute},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := reconnectFor(c.flag, c.cfg, c.ref)
			if err != nil {
				t.Fatalf("reconnectFor(%q, ref=%q): %v", c.flag, c.ref, err)
			}
			if got != c.want {
				t.Fatalf("reconnectFor(%q, ref=%q) = %s, want %s", c.flag, c.ref, got, c.want)
			}
		})
	}
}

// TestSingleLaneKnob locks in the measurement knob: 1 keeps the SSH
// transport on one lane, unset opens the lanes, and any other value is an
// error that names the variable.
func TestSingleLaneKnob(t *testing.T) {
	cases := []struct {
		value string
		want  bool
		ok    bool
	}{
		{"", false, true},
		{"1", true, true},
		{"0", false, false},
		{"true", false, false},
	}
	for _, c := range cases {
		t.Setenv("TJ_SSH_LANES", c.value)
		got, err := singleLaneKnob()
		if (err == nil) != c.ok {
			t.Fatalf("singleLaneKnob() with %q = %v, want ok=%v", c.value, err, c.ok)
		}
		if got != c.want {
			t.Fatalf("singleLaneKnob() with %q = %v, want %v", c.value, got, c.want)
		}
		if err != nil && !strings.Contains(err.Error(), "TJ_SSH_LANES") {
			t.Fatalf("error %q does not name TJ_SSH_LANES", err)
		}
	}
}
