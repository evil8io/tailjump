package session

import (
	"context"
	"errors"
	"fmt"
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

// Start writes the plan and starts the session. Without foreground it starts
// the transient unit and waits for the state file to report up; with
// foreground it runs the session in-process. Start runs as root.
func Start(ctx context.Context, planJSON []byte, foreground bool) error {
	plat := platform.New()
	planPath := PlanPath(plat.Paths.RuntimeDir())
	if err := writePlan(planPath, planJSON); err != nil {
		return err
	}
	if foreground {
		return Run(ctx, planPath)
	}
	if err := plat.Runner.Start(planPath); err != nil {
		return err
	}
	return waitForUp(ctx, plat)
}

func waitForUp(ctx context.Context, plat platform.Platform) error {
	statePath := StatePath(plat.Paths.RuntimeDir())
	start := time.Now()
	deadline := start.Add(upTimeout)
	for {
		if st, err := ReadState(statePath); err == nil && st.Status == StatusUp {
			_, _ = fmt.Fprintf(os.Stdout, "session to %s up, %d networks\n", st.Remote, len(st.Networks))
			return nil
		}
		active, err := plat.Runner.Active()
		if err == nil && !active && time.Since(start) > 3*time.Second {
			return errors.New("the session unit exited before it came up; see journalctl -u tj-session")
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %s waiting for the session to come up", upTimeout)
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
	addr, err := netip.ParseAddr(plan.Addr)
	if err != nil {
		return fmt.Errorf("plan addr %q: %w", plan.Addr, err)
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

	helperBytes, err := embed.Helper(plan.HelperArch)
	if err != nil {
		return err
	}
	helperPath, err := uploadHelper(sshClient, helperBytes)
	if err != nil {
		return err
	}
	slog.Debug("helper uploaded", "path", helperPath, "arch", plan.HelperArch)

	transport, err := sshClient.Exec("exec " + helperPath)
	if err != nil {
		return fmt.Errorf("start helper: %w", err)
	}
	muxClient, err := mux.NewClient(transport)
	if err != nil {
		return fmt.Errorf("open mux: %w", err)
	}
	defer func() { _ = muxClient.Close() }()
	slog.Debug("mux up", "helper", muxClient.Info().Hostname, "version", muxClient.Info().Version)

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

	dp, err := dataplane.New(dev, muxClient, deviceMTU)
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
	slog.Info("session up", "remote", plan.Remote, "networks", len(plan.Networks), "dns", plan.DNS.Mode)

	select {
	case <-ctx.Done():
		slog.Info("session stopping on signal")
	case <-muxClient.Wait():
		slog.Warn("session ended: mux closed")
	}

	state.Status = StatusStopping
	_ = writeState(statePath, state)
	revertDNS(plat, name)
	removeRoutes(plat, name, routes)
	if err := muxClient.Quit(); err != nil {
		slog.Debug("quit helper", "error", err)
	}
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

	if err := os.Remove(statePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.Warn("cleanup: remove state file", "error", err)
	}
	if err := os.Remove(PlanPath(runtimeDir)); err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.Warn("cleanup: remove plan file", "error", err)
	}
	slog.Info("cleanup complete")
	return nil
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
