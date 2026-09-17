package session

import (
	"context"
	"log/slog"
	"time"
)

// The metrics sampler: it probes the transport every metricsProbeInterval,
// gives each probe metricsProbeTimeout to answer, and sends one sample every
// metricsWriteInterval.
const (
	metricsProbeInterval = time.Second
	metricsWriteInterval = 5 * time.Second
	metricsProbeTimeout  = 2 * time.Second
)

// startMetrics starts the metrics sampler of the current transport and
// returns the function that stops it and waits for its goroutine. The data
// plane lives across a reconnect, so the totals continue over the gap.
func (r *runner) startMetrics(ctx context.Context) func() {
	samples := make(chan Metrics, 1)
	r.samples = samples
	wctx, cancel := context.WithCancel(ctx)
	ticker := time.NewTicker(metricsProbeInterval)
	w := &metricsWatch{
		tick:     ticker.C,
		now:      time.Now,
		probe:    r.rttFunc(),
		counters: r.dp.Counters,
		out:      samples,
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer ticker.Stop()
		w.run(wctx)
	}()
	return func() {
		cancel()
		<-done
	}
}

// rttFunc returns the round-trip probe of the current transport. It binds the
// client now, for the reason of probeFunc.
func (r *runner) rttFunc() func(context.Context) (time.Duration, error) {
	if r.quicClient != nil {
		return r.quicClient.RTT
	}
	return r.muxClient.RTT
}

// metricsWatch measures the session while it is up: the round-trip time of
// the transport and the byte counters of the data plane. It owns no state of
// the runner and reports every sample on a channel, because the runner
// goroutine is the only writer of the state file.
type metricsWatch struct {
	tick     <-chan time.Time
	now      func() time.Time
	probe    func(context.Context) (time.Duration, error)
	counters func() (up, down uint64)
	out      chan<- Metrics
}

// run probes on every tick and sends a sample on the first tick and on every
// write interval after it. A probe that does not answer is no loss signal:
// the mux keepalive and the resume detector report a dead transport, and a
// single lost probe under load is a normal event.
func (w *metricsWatch) run(ctx context.Context) {
	last := w.now()
	upBase, downBase := w.counters()
	var rttSum time.Duration
	var answered int
	first := true

	for {
		select {
		case <-ctx.Done():
			return
		case <-w.tick:
		}

		pctx, cancel := context.WithTimeout(ctx, metricsProbeTimeout)
		rtt, err := w.probe(pctx)
		cancel()
		if err != nil {
			slog.Debug("the metrics probe failed", "error", err)
		} else {
			rttSum += rtt
			answered++
		}

		now := w.now()
		elapsed := now.Sub(last)
		if !first && elapsed < metricsWriteInterval {
			continue
		}
		first = false

		up, down := w.counters()
		w.send(Metrics{
			UpdatedAt: now.UTC().Format(time.RFC3339),
			RTTMS:     meanMS(rttSum, answered),
			UpBytes:   up,
			DownBytes: down,
			UpRate:    rate(up-upBase, elapsed),
			DownRate:  rate(down-downBase, elapsed),
		})
		last, upBase, downBase = now, up, down
		rttSum, answered = 0, 0
	}
}

// send hands the sample to the runner, and drops it when the runner did not
// read the previous one. The runner reads no sample while it reconnects, and
// a stale sample is worth less than a wait.
func (w *metricsWatch) send(m Metrics) {
	select {
	case w.out <- m:
	default:
		slog.Debug("the runner did not read the last metrics sample")
	}
}

// meanMS is the mean of the answered probes in milliseconds, zero when no
// probe answered.
func meanMS(sum time.Duration, answered int) float64 {
	if answered == 0 {
		return 0
	}
	return float64(sum) / float64(answered) / float64(time.Millisecond)
}

// rate is the bytes per second of a counter delta over d.
func rate(delta uint64, d time.Duration) uint64 {
	if d <= 0 {
		return 0
	}
	return uint64(float64(delta) / d.Seconds())
}
