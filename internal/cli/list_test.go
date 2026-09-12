package cli

import (
	"testing"

	"github.com/evil8io/tailjump/internal/session"
	"github.com/evil8io/tailjump/internal/tailnet"
)

func TestPathOf(t *testing.T) {
	cases := []struct {
		peer tailnet.Peer
		want string
	}{
		{tailnet.Peer{Active: true, CurAddr: "[2001:db8::2]:41641", Relay: "lhr"}, "direct"},
		{tailnet.Peer{Active: true, Relay: "lhr"}, "relay lhr"},
		{tailnet.Peer{Active: false, Relay: "lhr"}, "idle"},
	}
	for _, c := range cases {
		if got := pathOf(c.peer).String(); got != c.want {
			t.Errorf("pathOf(%+v) = %q, want %q", c.peer, got, c.want)
		}
	}
	if got := (&pathInfo{Type: pathDirect, LatencyMS: 29.4}).String(); got != "direct 29ms" {
		t.Errorf("direct with latency = %q", got)
	}
	if got := (*pathInfo)(nil).String(); got != "-" {
		t.Errorf("nil path = %q", got)
	}
}

func TestSessionOf(t *testing.T) {
	st := &session.State{Addr: "100.64.0.10", Status: session.StatusUp}
	if got := sessionOf(st, "100.64.0.10"); got != "up" {
		t.Errorf("matching address = %q", got)
	}
	if got := sessionOf(st, "100.64.0.11"); got != "" {
		t.Errorf("other address = %q", got)
	}
	if got := sessionOf(nil, "100.64.0.10"); got != "" {
		t.Errorf("no session = %q", got)
	}
}
