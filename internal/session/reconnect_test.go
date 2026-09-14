package session

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/evil8io/tailjump/internal/mux"
	"github.com/evil8io/tailjump/internal/tailnet"
)

// atomicDialer counts the dials it serves. The other dialers of these tests
// count from one goroutine; this one is safe for the concurrent dials of the
// switch dialer test.
type atomicDialer struct{ calls atomic.Int64 }

func (d *atomicDialer) DialTCP(netip.AddrPort) (net.Conn, error) {
	d.calls.Add(1)
	return nil, nil
}

func (d *atomicDialer) DialUDP(netip.AddrPort) (*mux.UDPConn, error) {
	d.calls.Add(1)
	return nil, nil
}

func (d *atomicDialer) DialICMP(netip.Addr, uint16) (*mux.EchoConn, error) {
	d.calls.Add(1)
	return nil, nil
}

// TestSwitchDialerWithoutADialer checks that every dial fails at once while
// the session has no transport, so the netstack resets a new flow instead of
// holding the application.
func TestSwitchDialerWithoutADialer(t *testing.T) {
	var s switchDialer
	s.set(&atomicDialer{})
	s.clear()

	dst := netip.MustParseAddrPort("10.0.0.5:443")
	if _, err := s.DialTCP(dst); !errors.Is(err, errNoTransport) {
		t.Errorf("DialTCP error = %v, want %v", err, errNoTransport)
	}
	if _, err := s.DialUDP(dst); !errors.Is(err, errNoTransport) {
		t.Errorf("DialUDP error = %v, want %v", err, errNoTransport)
	}
	if _, err := s.DialICMP(dst.Addr(), 1); !errors.Is(err, errNoTransport) {
		t.Errorf("DialICMP error = %v, want %v", err, errNoTransport)
	}
}

// TestSwitchDialerSwapsUnderLoad dials from several goroutines while another
// installs and removes the dialer, the reconnect case. Every dial either
// reaches a dialer or reports that there is none.
func TestSwitchDialerSwapsUnderLoad(t *testing.T) {
	var s switchDialer
	first, second := &atomicDialer{}, &atomicDialer{}
	s.set(first)

	dst := netip.MustParseAddrPort("10.0.0.5:443")
	stop := make(chan struct{})
	var dialers sync.WaitGroup
	for range 4 {
		dialers.Go(func() {
			for range 2000 {
				if _, err := s.DialTCP(dst); err != nil && !errors.Is(err, errNoTransport) {
					t.Errorf("DialTCP error = %v", err)
					return
				}
				if _, err := s.DialUDP(dst); err != nil && !errors.Is(err, errNoTransport) {
					t.Errorf("DialUDP error = %v", err)
					return
				}
			}
		})
	}

	var swapper sync.WaitGroup
	swapper.Go(func() {
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			switch i % 3 {
			case 0:
				s.clear()
			case 1:
				s.set(first)
			case 2:
				s.set(second)
			}
		}
	})

	dialers.Wait()
	close(stop)
	swapper.Wait()

	if first.calls.Load()+second.calls.Load() == 0 {
		t.Fatal("no dial reached a dialer")
	}
}

func TestBackoffAfter(t *testing.T) {
	want := []time.Duration{
		time.Second,
		2 * time.Second,
		4 * time.Second,
		8 * time.Second,
		10 * time.Second,
		10 * time.Second,
		10 * time.Second,
	}
	for i, w := range want {
		if got := backoffAfter(i + 1); got != w {
			t.Errorf("backoffAfter(%d) = %s, want %s", i+1, got, w)
		}
	}
}

// fakeClock is the wall clock of a resume detector test. Every read reports
// itself on read, so the test knows the detector consumed the current time
// before it moves the clock on.
type fakeClock struct {
	mu   sync.Mutex
	t    time.Time
	read chan struct{}
}

func newFakeClock() *fakeClock {
	return &fakeClock{
		t:    time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC),
		read: make(chan struct{}, 8),
	}
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	t := c.t
	c.mu.Unlock()
	c.read <- struct{}{}
	return t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// step moves the clock on, ticks the detector, and waits until it has read
// the new time.
func (c *fakeClock) step(t *testing.T, tick chan<- time.Time, d time.Duration) {
	t.Helper()
	c.advance(d)
	tick <- time.Time{}
	<-c.read
}

// TestResumeWatchProbesAfterASuspend checks that only a wall-clock gap above
// the threshold sends a probe, and that it sends one probe for one gap.
func TestResumeWatchProbesAfterASuspend(t *testing.T) {
	clock := newFakeClock()
	tick := make(chan time.Time)
	loss := make(chan string, 1)
	var probes atomic.Int64
	w := &resumeWatch{
		tick: tick,
		now:  clock.now,
		probe: func(context.Context) error {
			probes.Add(1)
			return nil
		},
		loss: loss,
	}

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.run(ctx)
	}()
	<-clock.read

	clock.step(t, tick, time.Second)
	if got := probes.Load(); got != 0 {
		t.Fatalf("a one second gap sent %d probes, want 0", got)
	}
	clock.step(t, tick, 6*time.Second)
	clock.step(t, tick, time.Second)

	cancel()
	<-done
	if got := probes.Load(); got != 1 {
		t.Errorf("probes = %d, want 1", got)
	}
	select {
	case reason := <-loss:
		t.Errorf("an answered probe reported a loss: %s", reason)
	default:
	}
}

// TestResumeWatchFailedProbeIsALoss checks that a probe that does not answer
// after a suspend ends the transport.
func TestResumeWatchFailedProbeIsALoss(t *testing.T) {
	clock := newFakeClock()
	tick := make(chan time.Time)
	loss := make(chan string, 1)
	w := &resumeWatch{
		tick: tick,
		now:  clock.now,
		probe: func(context.Context) error {
			return errors.New("no answer")
		},
		loss: loss,
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		w.run(t.Context())
	}()
	<-clock.read

	clock.step(t, tick, 6*time.Second)
	<-done

	select {
	case reason := <-loss:
		if reason != "resume probe failed: no answer" {
			t.Errorf("loss reason = %q", reason)
		}
	default:
		t.Fatal("a failed probe reported no loss")
	}
}

func TestManifestMatches(t *testing.T) {
	body := []byte("version: 1\nnetworks:\n  - 10.0.0.0/16\n")
	sum := sha256.Sum256(body)
	want := hex.EncodeToString(sum[:])

	if !manifestMatches(want, body) {
		t.Error("the same manifest did not match")
	}
	if manifestMatches(want, []byte("version: 1\n")) {
		t.Error("another manifest matched")
	}

	none := sha256.Sum256(nil)
	if !manifestMatches(hex.EncodeToString(none[:]), nil) {
		t.Error("a remote without a manifest did not match the hash of zero bytes")
	}
	if manifestMatches(want, nil) {
		t.Error("a remote without a manifest matched a plan that has one")
	}
}

func TestResolveAddr(t *testing.T) {
	gw := tailnet.Peer{
		HostName:     "gw.example",
		Online:       true,
		TailscaleIPs: []netip.Addr{netip.MustParseAddr("100.64.0.10")},
	}
	moved := tailnet.Peer{
		HostName:     "gw.example-1",
		Online:       true,
		Tags:         []string{"tag:example"},
		TailscaleIPs: []netip.Addr{netip.MustParseAddr("100.64.0.11")},
	}
	self := tailnet.Peer{HostName: "client.example", Online: true}

	cases := []struct {
		name     string
		status   *tailnet.Status
		ref      string
		wantAddr string
		wantHost string
		wantSub  string
	}{
		{
			name:    "tailscaled offline",
			status:  &tailnet.Status{Self: tailnet.Peer{HostName: "client.example"}, Peers: []tailnet.Peer{gw}},
			ref:     "gw.example",
			wantSub: "tailscaled offline",
		},
		{
			name:    "no match",
			status:  &tailnet.Status{Self: self, Peers: []tailnet.Peer{gw}},
			ref:     "other.example",
			wantSub: "no online peer",
		},
		{
			name:     "the same address",
			status:   &tailnet.Status{Self: self, Peers: []tailnet.Peer{gw}},
			ref:      "gw.example",
			wantAddr: "100.64.0.10",
			wantHost: "gw.example",
		},
		{
			name:     "a moved address",
			status:   &tailnet.Status{Self: self, Peers: []tailnet.Peer{moved}},
			ref:      "tag:example",
			wantAddr: "100.64.0.11",
			wantHost: "gw.example-1",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			addr, host, reason := resolveAddr(c.status, c.ref)
			if c.wantSub != "" {
				if !strings.Contains(reason, c.wantSub) {
					t.Fatalf("reason = %q, want it to name %q", reason, c.wantSub)
				}
				return
			}
			if reason != "" {
				t.Fatalf("reason = %q, want none", reason)
			}
			if addr.String() != c.wantAddr || host != c.wantHost {
				t.Fatalf("resolveAddr = %s %s, want %s %s", addr, host, c.wantAddr, c.wantHost)
			}
		})
	}
}
