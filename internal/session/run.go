package session

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"golang.zx2c4.com/wireguard/tun"

	"github.com/evil8io/tailjump/internal/dataplane"
	"github.com/evil8io/tailjump/internal/dns"
	"github.com/evil8io/tailjump/internal/helper/embed"
	"github.com/evil8io/tailjump/internal/mux"
	"github.com/evil8io/tailjump/internal/platform"
	"github.com/evil8io/tailjump/internal/sshc"
)

// uploadScript writes the helper to the SSH user's runtime directory and
// prints its path. It mirrors docs/architecture.md, "Upload and start".
const uploadScript = `d="${XDG_RUNTIME_DIR:-$HOME/.cache/tj}"; mkdir -p "$d" && f="$d/tj-helper.$$" && cat > "$f" && chmod 0700 "$f" && echo "$f"`

// upTimeout bounds the wait for the session to report status up.
const upTimeout = 60 * time.Second

// cancelStopTimeout bounds the wait for the unit to stop after a cancelled
// connect.
const cancelStopTimeout = 25 * time.Second

// streamGrace is the time the verbose log stream runs on after the wait ends.
const streamGrace = time.Second

// failTailLines is the number of session log lines a failed connect prints.
const failTailLines = 20

// Start writes the plan and starts the session. Without foreground it starts
// the transient unit and waits for the state file to report up; with
// foreground it runs the session in-process. Start runs as root. It writes to
// errw only: the progress of the wait and the log tail of a failure. The
// caller prints the final line, because it has the start time of the command.
func Start(ctx context.Context, errw io.Writer, planJSON []byte, foreground bool) error {
	plat := platform.New()
	planPath := PlanPath(plat.Paths.RuntimeDir())
	if err := writePlan(planPath, planJSON); err != nil {
		return err
	}
	if foreground {
		return Run(ctx, planPath)
	}
	unitStart := time.Now()
	if err := plat.Runner.Start(planPath); err != nil {
		return err
	}
	if err := waitForUp(ctx, errw, plat, unitStart); err != nil {
		if ctx.Err() != nil {
			return stopCancelled(ctx, errw, plat)
		}
		return err
	}
	return nil
}

// stopCancelled stops the unit of a connect that a signal cancelled, and
// waits until the unit is inactive. The connect context is done, so the wait
// gets its own deadline. A session that already came up stays up, because the
// caller reaches this path on a failed wait only.
func stopCancelled(ctx context.Context, errw io.Writer, plat platform.Platform) error {
	if err := plat.Runner.Stop(); err != nil {
		slog.Warn("stop the cancelled session", "error", err)
	}
	wait, cancel := context.WithTimeout(context.WithoutCancel(ctx), cancelStopTimeout)
	defer cancel()
	if err := waitInactive(wait, plat, cancelStopTimeout); err != nil {
		slog.Warn("wait for the cancelled session to stop", "error", err)
	}
	_, _ = fmt.Fprintln(errw, "connect cancelled; the session is stopped")
	return ctx.Err()
}

// waitForUp waits until the session reports up. With debug logging on it
// streams the session log to errw during the wait, so the steps of the unit
// print as they happen. Without it a failure prints the last lines of that
// log instead. A cancelled wait prints neither: the caller stops the unit and
// reports the cancel.
func waitForUp(ctx context.Context, errw io.Writer, plat platform.Platform, unitStart time.Time) error {
	verbose := slog.Default().Enabled(ctx, slog.LevelDebug)
	var stop func()
	if verbose {
		stop = streamLog(ctx, errw, plat, unitStart)
	}
	err := awaitUp(ctx, plat)
	if verbose {
		// The unit writes the state file before journald has its last
		// lines, so the stream gets a moment to catch up.
		if ctx.Err() == nil {
			select {
			case <-time.After(streamGrace):
			case <-ctx.Done():
			}
		}
		stop()
	}
	if err != nil && ctx.Err() == nil && !verbose {
		if lerr := plat.Runner.Logs(ctx, errw, platform.LogOptions{Lines: failTailLines, Since: unitStart}); lerr != nil {
			slog.Debug("read the session log tail", "error", lerr)
		}
	}
	return err
}

// streamLog follows the session log to w in its own goroutine. The returned
// function cancels the stream and waits for the goroutine, so no line arrives
// after the caller returns.
func streamLog(ctx context.Context, w io.Writer, plat platform.Platform, since time.Time) func() {
	stream, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := plat.Runner.Logs(stream, w, platform.LogOptions{Follow: true, Since: since}); err != nil {
			slog.Debug("stream the session log", "error", err)
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

func awaitUp(ctx context.Context, plat platform.Platform) error {
	statePath := StatePath(plat.Paths.RuntimeDir())
	start := time.Now()
	deadline := start.Add(upTimeout)
	for {
		if st, err := ReadState(statePath); err == nil && st.Status == StatusUp {
			return nil
		}
		active, err := plat.Runner.Active()
		if err == nil && !active && time.Since(start) > 3*time.Second {
			return errors.New("the session unit exited before it came up; see tj logs for the full log")
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %s waiting for the session to come up; see tj logs for the full log", upTimeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// Run is the session itself: it opens SSH, starts the helper, opens the mux,
// creates the device, adds the routes, applies the DNS mode, writes the state
// file with status up, and waits for a signal or a mux failure. On exit it
// reverts every change. Run runs as root inside the transient unit.
func Run(ctx context.Context, planPath string) (err error) {
	// The transient unit's ExecStopPost runs Cleanup on any exit, including a
	// panic, but a recovered panic also gives a clean log line instead of a
	// raw stack trace in the journal.
	defer func() {
		if r := recover(); r != nil {
			slog.Error("session run panicked", "panic", r)
			err = fmt.Errorf("session run panicked: %v", r)
		}
	}()

	plat := platform.New()
	statePath := StatePath(plat.Paths.RuntimeDir())

	plan, err := ReadPlan(planPath)
	if err != nil {
		return err
	}
	if plan.Verbose {
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})))
	}
	addr, err := netip.ParseAddr(plan.Addr)
	if err != nil {
		return fmt.Errorf("plan addr %q: %w", plan.Addr, err)
	}
	set, err := plan.protocolSet()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	started := time.Now()
	state := &State{
		Remote:    plan.Remote,
		Addr:      plan.Addr,
		User:      plan.User,
		Networks:  plan.Networks,
		DNS:       plan.DNS,
		StartedAt: started.UTC().Format(time.RFC3339),
		PID:       os.Getpid(),
		Status:    StatusStarting,
		Protocols: set.String(),
	}
	if err := writeState(statePath, state); err != nil {
		return err
	}
	defer func() { _ = os.Remove(statePath) }()

	// A stale device from a prior crash would block the create.
	_ = plat.Device.Delete(deviceName)

	sshClient, err := sshc.Dial(ctx, addr, plan.Remote, plan.User, plat.Paths.CacheDir())
	if err != nil {
		return fmt.Errorf("ssh dial %s: %w", plan.Remote, err)
	}
	defer func() { _ = sshClient.Close() }()

	muxClient, helperPath, err := startHelper(sshClient, plan.HelperArch)
	if err != nil {
		return err
	}
	defer func() { _ = muxClient.Close() }()
	slog.Debug("mux up", "helper", muxClient.Info().Hostname, "version", muxClient.Info().Version)

	var dialer mux.Dialer
	var quicWait <-chan struct{}
	var lanes *laneSet
	quicClient, err := selectTransport(ctx, muxClient, addr, plan, state)
	if err != nil {
		return err
	}
	if quicClient != nil {
		defer func() { _ = quicClient.Close() }()
		dialer = quicClient
		quicWait = quicClient.Wait()
		unlinkHelper(muxClient)
	} else {
		lanes, err = openLaneSet(ctx, muxClient, addr, plan, set, plat.Paths.CacheDir(), helperPath)
		if err != nil {
			return err
		}
		defer lanes.stop()
		unlinkHelper(muxClient)
		dialer = lanes.dialer
		state.Lanes = lanes.names
	}

	dev, name, err := plat.Device.Create(deviceName, deviceMTU)
	if err != nil {
		return fmt.Errorf("create device: %w", err)
	}
	if err := plat.Device.Configure(name, deviceAddrs); err != nil {
		teardownDevice(plat, dev, name)
		return fmt.Errorf("configure device: %w", err)
	}

	routes, err := routePrefixes(plan)
	if err != nil {
		teardownDevice(plat, dev, name)
		return err
	}
	if err := plat.Router.Add(name, routes); err != nil {
		teardownDevice(plat, dev, name)
		return fmt.Errorf("add routes: %w", err)
	}

	if err := applyDNS(plat, name, plan); err != nil {
		removeRoutes(plat, name, routes)
		teardownDevice(plat, dev, name)
		return fmt.Errorf("apply dns: %w", err)
	}

	dp, err := dataplane.New(dev, dialer, deviceMTU, set)
	if err != nil {
		revertDNS(plat, name)
		removeRoutes(plat, name, routes)
		teardownDevice(plat, dev, name)
		return fmt.Errorf("start data plane: %w", err)
	}
	dp.Run(ctx)

	state.Status = StatusUp
	if err := writeState(statePath, state); err != nil {
		slog.Warn("write up state", "error", err)
	}
	slog.Info("session up", "remote", plan.Remote, "networks", len(plan.Networks), "dns", plan.DNS.Mode, "transport", state.Transport, "protocols", state.Protocols)
	stopWatch := watchTransportPath(ctx, addr)

	select {
	case <-ctx.Done():
		slog.Info("session stopping on signal")
	case <-muxClient.Wait():
		slog.Warn("session ended: mux closed")
	case name := <-lanes.closed():
		slog.Warn("session ended: lane mux closed", "lane", name)
	case <-quicWait:
		slog.Warn("session ended: quic connection closed")
	}

	state.Status = StatusStopping
	_ = writeState(statePath, state)
	stopWatch()
	revertDNS(plat, name)
	removeRoutes(plat, name, routes)
	resetRoutes(plat)
	if quicClient != nil {
		_ = quicClient.Close()
	}
	if err := muxClient.Quit(); err != nil {
		slog.Debug("quit helper", "error", err)
	}
	lanes.stop()
	_ = dp.Close()
	dp.Wait()
	teardownDevice(plat, nil, name)
	slog.Info("session down", "remote", plan.Remote)
	return nil
}

// teardownDevice deletes the device and logs a failure, so a setup or
// shutdown path that cannot delete it still leaves a diagnostic. dev is
// optional: a nil dev skips the Close, for the path where the device is
// already handed off to the data plane.
func teardownDevice(plat platform.Platform, dev tun.Device, name string) {
	if dev != nil {
		_ = dev.Close()
	}
	if err := plat.Device.Delete(name); err != nil {
		slog.Warn("delete device", "device", name, "error", err)
	}
}

// removeRoutes removes the session routes and logs a failure. Deleting the
// device also removes its routes, so this only matters on a setup path that
// backs out before the device is deleted.
func removeRoutes(plat platform.Platform, name string, routes []netip.Prefix) {
	if err := plat.Router.Remove(name, routes); err != nil {
		slog.Warn("remove routes", "device", name, "error", err)
	}
}

// resetRoutes removes the session rules and flushes the session table, and
// logs a failure. A device delete leaves the rules behind.
func resetRoutes(plat platform.Platform) {
	if err := plat.Router.Reset(); err != nil {
		slog.Warn("reset routes", "error", err)
	}
}

// revertDNS reverts the DNS mode and logs a failure. Revert is a no-op when
// no DNS mode was applied.
func revertDNS(plat platform.Platform, name string) {
	if err := plat.Resolver.Revert(name); err != nil {
		slog.Debug("revert dns", "device", name, "error", err)
	}
}

// Stop ends the transient unit. The unit's stop path reverts the session.
func Stop() error {
	return platform.New().Runner.Stop()
}

// Cleanup reverts every session change on the device and removes the runtime
// files. systemd runs it through ExecStopPost after every stop, including a
// crash or a kill signal, and it is safe to repeat: every step tolerates the
// change already being gone.
func Cleanup() error {
	return cleanup(platform.New())
}

func cleanup(plat platform.Platform) error {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("session cleanup panicked", "panic", r)
		}
	}()

	runtimeDir := plat.Paths.RuntimeDir()
	statePath := StatePath(runtimeDir)
	if st, err := ReadState(statePath); err == nil {
		slog.Info("cleanup", "remote", st.Remote, "status", st.Status)
	} else if !errors.Is(err, os.ErrNotExist) {
		slog.Debug("cleanup: read state", "error", err)
	}

	revertDNS(plat, deviceName)
	teardownDevice(plat, nil, deviceName)
	resetRoutes(plat)

	if err := os.Remove(statePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.Warn("cleanup: remove state file", "error", err)
	}
	if err := os.Remove(PlanPath(runtimeDir)); err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.Warn("cleanup: remove plan file", "error", err)
	}
	slog.Info("cleanup complete")
	return nil
}

// startHelper uploads the helper for the remote's architecture, starts it,
// opens the mux over its stdin and stdout, and returns the uploaded path. The
// extra lanes start their own helper from that same file.
func startHelper(client *sshc.Client, arch string) (*mux.Client, string, error) {
	helperBytes, err := embed.Helper(arch)
	if err != nil {
		return nil, "", err
	}
	helperPath, err := uploadHelper(client, helperBytes)
	if err != nil {
		return nil, "", err
	}
	slog.Debug("helper uploaded", "path", helperPath, "arch", arch)

	muxClient, err := execHelper(client, helperPath)
	if err != nil {
		return nil, "", err
	}
	return muxClient, helperPath, nil
}

// execHelper runs the uploaded helper on the connection and opens the mux
// over its stdin and stdout.
func execHelper(client *sshc.Client, helperPath string) (*mux.Client, error) {
	channel, err := client.Exec("exec " + helperPath)
	if err != nil {
		return nil, fmt.Errorf("start helper: %w", err)
	}
	muxClient, err := mux.NewClient(channel)
	if err != nil {
		_ = channel.Close()
		return nil, fmt.Errorf("open mux: %w", err)
	}
	return muxClient, nil
}

// unlinkHelper asks the helper to remove its own file and waits for the
// answer. Every lane has started its helper by then, and Linux keeps a
// running binary alive without its file, so nothing needs the file after
// this point. A failure is a warning: the helper removes the file at its
// exit, and the next helper sweeps a stale file.
func unlinkHelper(muxClient *mux.Client) {
	if err := muxClient.Unlink(); err != nil {
		slog.Warn("the helper did not remove its file", "error", err)
	}
}

func uploadHelper(client *sshc.Client, body []byte) (string, error) {
	out, err := client.Run(uploadScript, body)
	if err != nil {
		return "", fmt.Errorf("upload helper: %w", err)
	}
	path := strings.TrimSpace(string(out))
	if path == "" {
		return "", errors.New("upload helper: the remote returned an empty path")
	}
	return path, nil
}

func applyDNS(plat platform.Platform, device string, plan *Plan) error {
	mode := dns.Mode(plan.DNS.Mode)
	if mode == dns.ModeNone {
		return nil
	}
	servers, err := parseAddrs(plan.DNS.Servers)
	if err != nil {
		return err
	}
	switch mode {
	case dns.ModeSplit:
		return plat.Resolver.ApplySplit(device, servers, plan.DNS.Domains)
	case dns.ModeAll:
		return plat.Resolver.ApplyAll(device, servers, plan.DNS.Domains)
	default:
		return fmt.Errorf("unknown dns mode %q", mode)
	}
}

func parseAddrs(list []string) ([]netip.Addr, error) {
	out := make([]netip.Addr, 0, len(list))
	for _, s := range list {
		a, err := netip.ParseAddr(s)
		if err != nil {
			return nil, fmt.Errorf("parse dns server %q: %w", s, err)
		}
		out = append(out, a)
	}
	return out, nil
}
