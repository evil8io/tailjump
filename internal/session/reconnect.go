package session

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/evil8io/tailjump/internal/discovery"
	"github.com/evil8io/tailjump/internal/mux"
	"github.com/evil8io/tailjump/internal/sshc"
	"github.com/evil8io/tailjump/internal/tailnet"
)

// The reconnect backoff: the first attempt runs at once, and the wait
// between the attempts doubles from one second to the cap.
const (
	backoffFirst = time.Second
	backoffCap   = 10 * time.Second
)

// The resume detector: it ticks every second and reads the wall clock, a gap
// above resumeGap is a suspend, and the probe that follows it has
// resumeProbeTimeout to answer.
const (
	resumeInterval     = time.Second
	resumeGap          = 5 * time.Second
	resumeProbeTimeout = 3 * time.Second
)

// errNoTransport is the dial error while the session has no transport.
var errNoTransport = errors.New("the session has no transport")

// errRemoteChanged ends the session: the remote at the new address serves a
// different manifest, so its session networks are not the networks of the
// plan.
var errRemoteChanged = errors.New("remote changed")

// switchDialer is the dialer the data plane holds for the life of the
// session. It points at the dialer of the current transport, and at none
// while the session reconnects. A dial without a dialer fails at once, so
// the netstack resets a new TCP flow and the application reports the failure
// instead of waiting. A flow that is already open ends with its stream.
type switchDialer struct {
	mu      sync.RWMutex
	current mux.Dialer
}

var _ mux.Dialer = (*switchDialer)(nil)

func (s *switchDialer) set(d mux.Dialer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.current = d
}

func (s *switchDialer) clear() {
	s.set(nil)
}

func (s *switchDialer) dialer() mux.Dialer {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.current
}

func (s *switchDialer) DialTCP(dst netip.AddrPort) (net.Conn, error) {
	d := s.dialer()
	if d == nil {
		return nil, errNoTransport
	}
	return d.DialTCP(dst)
}

func (s *switchDialer) DialUDP(dst netip.AddrPort) (*mux.UDPConn, error) {
	d := s.dialer()
	if d == nil {
		return nil, errNoTransport
	}
	return d.DialUDP(dst)
}

func (s *switchDialer) DialICMP(dst netip.Addr, ident uint16) (*mux.EchoConn, error) {
	d := s.dialer()
	if d == nil {
		return nil, errNoTransport
	}
	return d.DialICMP(dst, ident)
}

// backoffAfter returns the wait after attempt n: one second, then a double
// of the previous wait up to the cap.
func backoffAfter(attempt int) time.Duration {
	d := backoffFirst
	for i := 1; i < attempt; i++ {
		d *= 2
		if d >= backoffCap {
			return backoffCap
		}
	}
	return d
}

// sleepCtx waits for d and reports whether the wait finished. A done context
// ends it at once, so a stop signal wins over the backoff.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// manifestMatches reports whether content hashes to the manifest hash of the
// plan. A remote without a manifest returns no content, and the hash of zero
// bytes then matches a plan that connect built the same way.
func manifestMatches(want string, content []byte) bool {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:]) == want
}

// resolveAddr picks the remote's IPv4 tailnet address out of a tailscaled
// status. The second result is the hostname of the peer, and the third is
// the reason the remote is not reachable now, empty on a match. Self.Online
// is tailscaled's in-map-poll state: a link change restarts the poll, so it
// is false within seconds of a link going down, and an attempt then skips
// the dial.
func resolveAddr(st *tailnet.Status, ref string) (netip.Addr, string, string) {
	if !st.Self.Online {
		return netip.Addr{}, "", "tailscaled offline"
	}
	peer, err := tailnet.Resolve(st.Peers, ref)
	if err != nil {
		return netip.Addr{}, "", err.Error()
	}
	addr, err := peer.IPv4()
	if err != nil {
		return netip.Addr{}, "", err.Error()
	}
	return addr, peer.HostName, ""
}

// resumeWatch reports a loss that follows a suspend. The transport of a
// machine that slept can be dead without a close on either side, because no
// packet crossed the link while the clock ran on. A wall-clock gap between
// two ticks marks that sleep, and one probe then decides.
type resumeWatch struct {
	tick  <-chan time.Time
	now   func() time.Time
	probe func(context.Context) error
	loss  chan<- string
}

// run ticks until the context ends or the probe after a suspend fails. Sub
// on two times that both carry a monotonic reading uses the monotonic clock,
// and that clock stops during a suspend, so each reading loses its monotonic
// part first.
func (w *resumeWatch) run(ctx context.Context) {
	last := w.now().Round(0)
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.tick:
		}
		now := w.now().Round(0)
		gap := now.Sub(last)
		last = now
		if gap <= resumeGap {
			continue
		}
		slog.Debug("the clock jumped, probing the transport", "gap", gap.Round(time.Second))
		pctx, cancel := context.WithTimeout(ctx, resumeProbeTimeout)
		err := w.probe(pctx)
		cancel()
		if err == nil {
			continue
		}
		select {
		case w.loss <- fmt.Sprintf("resume probe failed: %v", err):
		case <-ctx.Done():
		}
		return
	}
}

// lost tears the current transport down and records the loss. The device,
// the routes, and the data plane stay, so the session keeps its addresses
// and its routes while it rebuilds the transport.
func (r *runner) lost(reason string) {
	slog.Warn(fmt.Sprintf("session lost: %s", reason))
	r.lostAt = time.Now()
	r.dialer.clear()
	r.stopWatch()
	r.stopResume()
	r.dropTransport()
	revertDNS(r.plat, r.device)
	r.state.Status = StatusReconnecting
	r.state.Reconnect = &ReconnectState{
		Since:  r.lostAt.UTC().Format(time.RFC3339),
		Reason: reason,
	}
	r.writeState()
}

// reconnect rebuilds the transport inside the window. It reports whether the
// session is up again. A false without an error means a signal stopped the
// session or the window passed, and the caller then takes the stop path.
//
// The window runs on the monotonic clock as it is: that clock stops during a
// suspend, so the window counts the time the machine was awake, and a
// session that slept longer than the window still gets its attempts.
func (r *runner) reconnect(ctx context.Context) (bool, error) {
	deadline := r.lostAt.Add(time.Duration(r.plan.ReconnectFor) * time.Second)
	for attempt := 1; ; attempt++ {
		if attempt > 1 && !sleepCtx(ctx, backoffAfter(attempt-1)) {
			return false, nil
		}
		if !time.Now().Before(deadline) {
			slog.Warn(fmt.Sprintf("reconnect gave up after %s", r.sinceLoss()))
			return false, nil
		}

		r.state.Reconnect.Attempts = attempt
		r.writeState()
		slog.Info(fmt.Sprintf("reconnect attempt %d", attempt))

		// The attempt runs under the window, so a dial that hangs ends with
		// the window instead of outliving it.
		actx, cancel := context.WithDeadline(ctx, deadline)
		reason, err := r.attempt(actx)
		cancel()

		if reason == "" && err == nil {
			r.state.Reconnects++
			r.markUp()
			slog.Info(fmt.Sprintf("session reconnected after %s (attempt %d)", r.sinceLoss(), attempt))
			r.startWatches(ctx)
			return true, nil
		}
		if err == nil && ctx.Err() != nil {
			return false, nil
		}
		slog.Warn(fmt.Sprintf("reconnect attempt %d failed: %s", attempt, reason))
		r.state.Reconnect.Reason = reason
		r.writeState()
		if err != nil {
			return false, err
		}
	}
}

// attempt runs one reconnect attempt: it resolves the remote again and
// rebuilds the transport. An empty reason means the session is up. An error
// ends the session, and the reason then names it too.
func (r *runner) attempt(ctx context.Context) (string, error) {
	if reason := r.resolve(ctx); reason != "" {
		return reason, nil
	}
	err := r.transport(ctx)
	switch {
	case err == nil:
		return "", nil
	case errors.Is(err, errRemoteChanged):
		return errRemoteChanged.Error(), err
	default:
		return err.Error(), nil
	}
}

// resolve finds the remote again and follows a move. It returns the reason
// the remote is not reachable now, and an empty reason when the address is
// ready to dial.
func (r *runner) resolve(ctx context.Context) string {
	st, err := tailnet.New("").Status(ctx)
	if err != nil {
		return fmt.Sprintf("tailscaled status: %v", err)
	}
	addr, hostname, reason := resolveAddr(st, r.plan.Ref)
	if reason != "" {
		return reason
	}
	if addr == r.addr {
		return ""
	}
	slog.Info(fmt.Sprintf("remote moved from %s to %s", r.addr, addr))
	r.addr, r.hostname = addr, hostname
	r.state.Addr, r.state.Remote = addr.String(), hostname
	r.writeState()
	return ""
}

// guardManifest checks that the remote at the current address is the remote
// the plan describes. It runs after a move only, because the manifest at the
// planned address is the manifest connect hashed. Another manifest means
// other session networks, so the session ends instead of forwarding the
// planned networks to a remote that does not serve them.
func (r *runner) guardManifest(client *sshc.Client) error {
	if r.addr.String() == r.plan.Addr {
		return nil
	}
	_, content, err := discovery.FetchManifest(func(script string) ([]byte, error) {
		return client.Run("sh", []byte(script))
	})
	if err != nil {
		return err
	}
	if !manifestMatches(r.plan.ManifestSHA256, content) {
		return fmt.Errorf("%w: the manifest of %s differs; run tj connect again", errRemoteChanged, r.hostname)
	}
	return nil
}

// startWatches starts the flap watch and the resume detector of the current
// transport. The resume detector runs with a reconnect window only: without
// one, a failed probe would end a session that no other signal calls lost.
func (r *runner) startWatches(ctx context.Context) {
	r.stopWatch = watchTransportPath(ctx, r.addr)
	r.stopResume = func() {}
	if r.plan.ReconnectFor <= 0 {
		return
	}

	loss := make(chan string, 1)
	r.resumeLoss = loss
	wctx, cancel := context.WithCancel(ctx)
	ticker := time.NewTicker(resumeInterval)
	w := &resumeWatch{
		tick:  ticker.C,
		now:   time.Now,
		probe: r.probeFunc(),
		loss:  loss,
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer ticker.Stop()
		w.run(wctx)
	}()
	r.stopResume = func() {
		cancel()
		<-done
	}
}

// probeFunc returns the liveness probe of the current transport. It binds
// the client now, so the detector's goroutine does not read a field that a
// reconnect replaces.
func (r *runner) probeFunc() func(context.Context) error {
	if r.quicClient != nil {
		return r.quicClient.Probe
	}
	return r.muxClient.Ping
}

// sinceLoss is the time since the loss, rounded to a second.
func (r *runner) sinceLoss() time.Duration {
	return time.Since(r.lostAt).Round(time.Second)
}
