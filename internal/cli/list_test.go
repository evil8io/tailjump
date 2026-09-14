package cli

import (
	"context"
	"fmt"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/evil8io/tailjump/internal/config"
	"github.com/evil8io/tailjump/internal/session"
	"github.com/evil8io/tailjump/internal/tailnet"
)

func TestPathOf(t *testing.T) {
	cases := []struct {
		peer tailnet.Peer
		want string
	}{
		{tailnet.Peer{Active: true, CurAddr: "[2001:db8::2]:41641", Relay: "xyz"}, "direct"},
		{tailnet.Peer{Active: true, Relay: "xyz"}, "relay xyz"},
		{tailnet.Peer{Active: false, Relay: "xyz"}, "idle"},
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

// TestProbeEntriesParallel checks that probeEntries runs the probes in
// parallel, at most probeConcurrency at a time, and keeps the output in the
// order of the entries.
func TestProbeEntriesParallel(t *testing.T) {
	origProbePeer := probePeer
	t.Cleanup(func() { probePeer = origProbePeer })

	var inFlight, maxInFlight int32
	probePeer = func(_ context.Context, p tailnet.Peer, _ netip.Addr, _ *config.Config, _ string) (bool, error) {
		n := atomic.AddInt32(&inFlight, 1)
		for {
			cur := atomic.LoadInt32(&maxInFlight)
			if n <= cur || atomic.CompareAndSwapInt32(&maxInFlight, cur, n) {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
		atomic.AddInt32(&inFlight, -1)
		var idx int
		_, _ = fmt.Sscanf(p.HostName, "peer%d", &idx)
		return idx%2 == 0, nil
	}

	const n = 20
	peers := make([]tailnet.Peer, n)
	entries := make([]listEntry, n)
	for i := 0; i < n; i++ {
		host := fmt.Sprintf("peer%d", i)
		addr := netip.MustParseAddr(fmt.Sprintf("100.64.0.%d", i+1))
		peers[i] = tailnet.Peer{HostName: host, TailscaleIPs: []netip.Addr{addr}}
		entries[i] = listEntry{HostName: host, Address: addr.String()}
	}

	start := time.Now()
	probeEntries(context.Background(), peers, entries, &config.Config{}, "")
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("probeEntries took %s for %d peers at 100ms each, want under 1s", elapsed, n)
	}

	if got := atomic.LoadInt32(&maxInFlight); got > probeConcurrency {
		t.Errorf("max concurrent probes = %d, want at most %d", got, probeConcurrency)
	}

	for i, e := range entries {
		if e.HostName != fmt.Sprintf("peer%d", i) {
			t.Fatalf("entries[%d].HostName = %q, order not kept", i, e.HostName)
		}
		want := i%2 == 0
		if e.Manifest == nil || *e.Manifest != want {
			t.Errorf("entries[%d].Manifest = %v, want %v", i, e.Manifest, want)
		}
	}
}
