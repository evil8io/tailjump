package protocols

import "testing"

func TestParseCanonicalOrder(t *testing.T) {
	cases := map[string]string{
		"tcp,udp,icmp":   "tcp,udp,icmp",
		"icmp,tcp":       "tcp,icmp",
		" udp , tcp ":    "tcp,udp",
		"tcp":            "tcp",
		"icmp":           "icmp",
		"udp,icmp":       "udp,icmp",
		"tcp,udp":        "tcp,udp",
		"tcp,icmp,udp":   "tcp,udp,icmp",
		"udp":            "udp",
		"icmp,udp,tcp":   "tcp,udp,icmp",
		"tcp, udp, icmp": "tcp,udp,icmp",
	}
	for in, want := range cases {
		set, err := Parse(in)
		if err != nil {
			t.Fatalf("Parse(%q): %v", in, err)
		}
		if got := set.String(); got != want {
			t.Fatalf("Parse(%q).String() = %q, want %q", in, got, want)
		}
	}
}

func TestParseRejects(t *testing.T) {
	for _, in := range []string{"", " ", "tcp,", "tcp,tcp", "sctp", "tcp,udp,gre", ","} {
		if _, err := Parse(in); err == nil {
			t.Fatalf("Parse(%q) returned no error", in)
		}
		if Valid(in) {
			t.Fatalf("Valid(%q) = true", in)
		}
	}
}

func TestResolvePrecedence(t *testing.T) {
	cases := []struct {
		flag, remote, defaults, want string
	}{
		{"", "", "", "tcp,udp,icmp"},
		{"", "", "tcp", "tcp"},
		{"", "tcp,udp", "tcp", "tcp,udp"},
		{"icmp", "tcp,udp", "tcp", "icmp"},
		{"tcp,udp,icmp", "", "", "tcp,udp,icmp"},
	}
	for _, c := range cases {
		set, err := Resolve(c.flag, c.remote, c.defaults)
		if err != nil {
			t.Fatalf("Resolve(%q, %q, %q): %v", c.flag, c.remote, c.defaults, err)
		}
		if got := set.String(); got != c.want {
			t.Fatalf("Resolve(%q, %q, %q) = %q, want %q", c.flag, c.remote, c.defaults, got, c.want)
		}
	}
}

func TestResolveReportsTheBadValue(t *testing.T) {
	if _, err := Resolve("", "gre", "tcp"); err == nil {
		t.Fatal("Resolve with an invalid remote value returned no error")
	}
}

func TestAll(t *testing.T) {
	if got := All().String(); got != "tcp,udp,icmp" {
		t.Fatalf("All() = %q", got)
	}
}
