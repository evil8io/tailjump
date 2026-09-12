package transport

import "testing"

func TestParsePortRange(t *testing.T) {
	cases := []struct {
		in   string
		want PortRange
		ok   bool
	}{
		{"7443-7452", PortRange{7443, 7452}, true},
		{"7443", PortRange{7443, 7443}, true},
		{" 1-65535 ", PortRange{1, 65535}, true},
		{"7452-7443", PortRange{}, false},
		{"0-10", PortRange{}, false},
		{"70000", PortRange{}, false},
		{"a-b", PortRange{}, false},
		{"", PortRange{}, false},
	}
	for _, c := range cases {
		got, err := ParsePortRange(c.in)
		if (err == nil) != c.ok {
			t.Fatalf("ParsePortRange(%q) err = %v, want ok=%v", c.in, err, c.ok)
		}
		if c.ok && got != c.want {
			t.Fatalf("ParsePortRange(%q) = %v, want %v", c.in, got, c.want)
		}
	}
	if got := DefaultPorts.String(); got != "7443-7452" {
		t.Fatalf("DefaultPorts = %q", got)
	}
	if got := DefaultPorts.PolicyRule(); got != "udp:7443-7452" {
		t.Fatalf("PolicyRule = %q", got)
	}
	if !DefaultPorts.Contains(7452) || DefaultPorts.Contains(7453) {
		t.Fatal("Contains is wrong at the range edge")
	}
}

func TestParseRate(t *testing.T) {
	cases := []struct {
		in   string
		want uint64
		ok   bool
	}{
		{"20 mbps", 2_500_000, true},
		{"20mbps", 2_500_000, true},
		{"50 Mbps", 6_250_000, true},
		{"8 bps", 1, true},
		{"1 kbps", 125, true},
		{"1 gbps", 125_000_000, true},
		{"1 tbps", 125_000_000_000, true},
		{"20", 0, false},
		{"mbps", 0, false},
		{"0 mbps", 0, false},
		{"20 mb", 0, false},
		{"", 0, false},
	}
	for _, c := range cases {
		got, err := ParseRate(c.in)
		if (err == nil) != c.ok {
			t.Fatalf("ParseRate(%q) err = %v, want ok=%v", c.in, err, c.ok)
		}
		if got != c.want {
			t.Fatalf("ParseRate(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestController(t *testing.T) {
	if got := ControllerFor(0); got.String() != "bbr" {
		t.Fatalf("ControllerFor(0) = %q", got)
	}
	if got := ControllerFor(2_500_000); got.String() != "brutal=2500000" {
		t.Fatalf("ControllerFor(2500000) = %q", got)
	}
	for _, s := range []string{"bbr", "brutal=2500000"} {
		c, err := ParseController(s)
		if err != nil {
			t.Fatalf("ParseController(%q): %v", s, err)
		}
		if c.String() != s {
			t.Fatalf("round trip %q = %q", s, c)
		}
	}
	for _, s := range []string{"", "cubic", "brutal", "brutal=0", "brutal=x", "bbr=1"} {
		if _, err := ParseController(s); err == nil {
			t.Fatalf("ParseController(%q) returned no error", s)
		}
	}
}

func TestResolve(t *testing.T) {
	if got := Resolve("", "", ""); got != ModeAuto {
		t.Fatalf("Resolve empty = %q", got)
	}
	if got := Resolve("", "ssh", "quic"); got != ModeSSH {
		t.Fatalf("remote config must win over defaults, got %q", got)
	}
	if got := Resolve("quic", "ssh", ""); got != ModeQUIC {
		t.Fatalf("the flag must win, got %q", got)
	}
	for _, s := range []string{"auto", "quic", "ssh"} {
		if !Valid(s) {
			t.Fatalf("Valid(%q) = false", s)
		}
	}
	if Valid("tcp") || Valid("") {
		t.Fatal("Valid accepted a bad mode")
	}
}
