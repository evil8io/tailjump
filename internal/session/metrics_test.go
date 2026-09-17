package session

import (
	"context"
	"errors"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/evil8io/tailjump/internal/platform"
	"github.com/evil8io/tailjump/internal/platform/fake"
	"github.com/evil8io/tailjump/internal/protocols"
)

// metricsHarness runs a sampler on the fake clock of the resume tests. The
// test moves the counters, ticks the sampler, and reads its samples.
type metricsHarness struct {
	clock   *fakeClock
	tick    chan time.Time
	out     chan Metrics
	counted chan struct{}
	up      atomic.Uint64
	down    atomic.Uint64
	stop    func()
}

// newMetricsHarness starts a sampler and waits until it has its baseline, so
// a counter the test moves next is a delta of the first sample.
func newMetricsHarness(t *testing.T, out chan Metrics, probe func(context.Context) (time.Duration, error)) *metricsHarness {
	t.Helper()
	h := &metricsHarness{
		clock:   newFakeClock(),
		tick:    make(chan time.Time),
		out:     out,
		counted: make(chan struct{}, 8),
	}
	w := &metricsWatch{
		tick:     h.tick,
		now:      h.clock.now,
		probe:    probe,
		counters: h.counters,
		out:      out,
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.run(ctx)
	}()
	h.stop = func() {
		cancel()
		<-done
	}
	t.Cleanup(h.stop)
	<-h.clock.read
	<-h.counted
	return h
}

func (h *metricsHarness) counters() (uint64, uint64) {
	defer func() { h.counted <- struct{}{} }()
	return h.up.Load(), h.down.Load()
}

// step adds to the counters, moves the clock on, and ticks the sampler.
func (h *metricsHarness) step(t *testing.T, d time.Duration, up, down uint64) {
	t.Helper()
	h.up.Add(up)
	h.down.Add(down)
	h.clock.step(t, h.tick, d)
}

// sample waits for the next sample of the sampler.
func (h *metricsHarness) sample(t *testing.T) Metrics {
	t.Helper()
	select {
	case m := <-h.out:
		return m
	case <-time.After(5 * time.Second):
		t.Fatal("no metrics sample within 5s")
		return Metrics{}
	}
}

// okProbe is a probe that always answers after rtt.
func okProbe(rtt time.Duration) func(context.Context) (time.Duration, error) {
	return func(context.Context) (time.Duration, error) { return rtt, nil }
}

// TestMetricsWatchSendsTheFirstSampleAtOnce checks that the first probe ends
// in a sample, so tj status has metrics one probe interval after the session
// comes up.
func TestMetricsWatchSendsTheFirstSampleAtOnce(t *testing.T) {
	h := newMetricsHarness(t, make(chan Metrics, 1), okProbe(20*time.Millisecond))

	h.step(t, time.Second, 1000, 4000)
	m := h.sample(t)

	if m.UpBytes != 1000 || m.DownBytes != 4000 {
		t.Errorf("totals = %d up, %d down, want 1000 and 4000", m.UpBytes, m.DownBytes)
	}
	if m.UpRate != 1000 || m.DownRate != 4000 {
		t.Errorf("rates = %d up, %d down, want the delta over one second", m.UpRate, m.DownRate)
	}
	if m.RTTMS != 20 {
		t.Errorf("rtt = %v ms, want 20", m.RTTMS)
	}
	if m.UpdatedAt == "" {
		t.Error("the sample has no time")
	}
}

// TestMetricsWatchSamplesOnTheWriteInterval checks that the samples after the
// first one follow the write interval, and that the rates use the counter
// deltas over the elapsed time.
func TestMetricsWatchSamplesOnTheWriteInterval(t *testing.T) {
	h := newMetricsHarness(t, make(chan Metrics, 1), okProbe(20*time.Millisecond))

	h.step(t, time.Second, 1000, 4000)
	h.sample(t)

	for range 4 {
		h.step(t, time.Second, 500, 1000)
		select {
		case m := <-h.out:
			t.Fatalf("a sample arrived inside the write interval: %+v", m)
		default:
		}
	}
	h.step(t, time.Second, 500, 1000)
	m := h.sample(t)

	if m.UpBytes != 3500 || m.DownBytes != 9000 {
		t.Errorf("totals = %d up, %d down, want 3500 and 9000", m.UpBytes, m.DownBytes)
	}
	if m.UpRate != 500 || m.DownRate != 1000 {
		t.Errorf("rates = %d up, %d down, want the deltas of 2500 and 5000 over five seconds", m.UpRate, m.DownRate)
	}
}

// TestMetricsWatchMeanRTTSkipsFailedProbes checks that a sample reports the
// mean of the probes that answered, and no round-trip time when none did.
func TestMetricsWatchMeanRTTSkipsFailedProbes(t *testing.T) {
	results := []struct {
		rtt time.Duration
		err error
	}{
		{rtt: 10 * time.Millisecond},
		{rtt: 30 * time.Millisecond},
		{err: errors.New("no answer")},
		{rtt: 50 * time.Millisecond},
		{err: errors.New("no answer")},
		{err: errors.New("no answer")},
	}
	var calls atomic.Int64
	probe := func(context.Context) (time.Duration, error) {
		i := int(calls.Add(1)) - 1
		if i >= len(results) {
			return 0, errors.New("no answer")
		}
		return results[i].rtt, results[i].err
	}
	h := newMetricsHarness(t, make(chan Metrics, 1), probe)

	h.step(t, time.Second, 0, 0)
	if m := h.sample(t); m.RTTMS != 10 {
		t.Errorf("first sample rtt = %v ms, want 10", m.RTTMS)
	}

	for range 5 {
		h.step(t, time.Second, 0, 0)
	}
	if m := h.sample(t); m.RTTMS != 40 {
		t.Errorf("rtt = %v ms, want the mean of the two answered probes, 40", m.RTTMS)
	}

	for range 5 {
		h.step(t, time.Second, 0, 0)
	}
	if m := h.sample(t); m.RTTMS != 0 {
		t.Errorf("rtt = %v ms, want none after five failed probes", m.RTTMS)
	}
}

// TestMetricsWatchDropsASampleWhenTheChannelIsFull checks that a runner that
// reads no sample, the reconnect case, neither blocks the sampler nor loses
// the samples that follow.
func TestMetricsWatchDropsASampleWhenTheChannelIsFull(t *testing.T) {
	out := make(chan Metrics, 1)
	out <- Metrics{UpdatedAt: "unread"}
	h := newMetricsHarness(t, out, okProbe(20*time.Millisecond))

	// The sample of the first tick and the sample five ticks later both find
	// the channel full. The tick after them proves the sampler is back at its
	// select, so the read below cannot race with a send.
	for range 6 {
		h.step(t, time.Second, 100, 200)
	}
	h.step(t, time.Second, 100, 200)
	select {
	case m := <-out:
		if m.UpdatedAt != "unread" {
			t.Fatalf("the unread sample was replaced by %+v", m)
		}
	default:
		t.Fatal("the unread sample is gone")
	}

	for range 4 {
		h.step(t, time.Second, 100, 200)
	}
	if m := h.sample(t); m.UpBytes != 1100 {
		t.Errorf("up = %d, want the total of every tick, 1100", m.UpBytes)
	}
}

// TestMetricsWatchStopEndsTheGoroutine checks that the stop of the sampler
// waits for its goroutine, so a reconnect starts the next one alone.
func TestMetricsWatchStopEndsTheGoroutine(t *testing.T) {
	h := newMetricsHarness(t, make(chan Metrics, 1), okProbe(20*time.Millisecond))
	h.stop()
}

// TestRunnerWaitStoresASample checks the single writer rule: the runner
// goroutine takes the sample, writes the state file, and keeps waiting.
func TestRunnerWaitStoresASample(t *testing.T) {
	dir := t.TempDir()
	plat := platform.Platform{
		Runner: &fake.Runner{},
		Paths:  &fake.Paths{RuntimeDirValue: dir},
	}
	r := newRunner(plat, samplePlan(), netip.MustParseAddr("100.64.0.10"), protocols.All())
	samples := make(chan Metrics, 1)
	r.samples = samples

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, lost := r.wait(ctx); lost {
			t.Error("the wait reported a loss")
		}
	}()

	want := Metrics{UpdatedAt: "2026-09-17T10:00:00Z", RTTMS: 12.5, UpBytes: 1000, DownBytes: 2000, UpRate: 100, DownRate: 200}
	samples <- want

	deadline := time.Now().Add(5 * time.Second)
	for {
		st, err := ReadState(r.statePath)
		if err == nil && st.Metrics != nil {
			if *st.Metrics != want {
				t.Fatalf("state file metrics = %+v, want %+v", *st.Metrics, want)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the state file got no metrics within 5s")
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	<-done
	if r.state.Metrics == nil || *r.state.Metrics != want {
		t.Errorf("runner metrics = %+v, want %+v", r.state.Metrics, want)
	}
}
