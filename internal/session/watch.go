package session

import (
	"context"
	"log/slog"
	"net/netip"
	"time"

	"github.com/evil8io/tailjump/internal/tailnet"
)

// The watch samples tailscaled's endpoint for the remote and warns when the
// path flaps. A loop through the session flaps every 15 to 20 s, so the
// sample interval is shorter than that, and three changes inside the window
// separate a flap from one direct-path discovery.
const (
	watchInterval = 10 * time.Second
	watchWindow   = 2 * time.Minute
	watchFlaps    = 3
)

// watchTransportPath logs a warning when tailscaled's path to the remote
// flaps. A session route that captures tailscaled's own packets produces
// that flap, and the rig cannot reproduce it, because tailscaled runs
// outside the rig's network namespace. The returned function stops the
// watch.
func watchTransportPath(ctx context.Context, addr netip.Addr) func() {
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		tc := tailnet.New("")
		var samples []string
		warned := false
		ticker := time.NewTicker(watchInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			st, err := tc.Status(ctx)
			if err != nil {
				slog.Debug("watch transport path", "error", err)
				continue
			}
			samples = append(samples, endpointOf(st, addr))
			if n := int(watchWindow / watchInterval); len(samples) > n {
				samples = samples[len(samples)-n:]
			}
			changes := pathChanges(samples)
			switch {
			case changes >= watchFlaps && !warned:
				slog.Warn("the tailscale path to the remote flaps; a session route may capture tailscaled's own packets",
					"changes", changes, "window", watchWindow, "remote", addr)
				warned = true
			case changes == 0:
				warned = false
			}
		}
	}()
	return cancel
}

// endpointOf returns the current endpoint of the peer with the tailnet
// address, or an empty string for a relayed or an unknown path.
func endpointOf(st *tailnet.Status, addr netip.Addr) string {
	for _, p := range st.Peers {
		if hasAddr(p.TailscaleIPs, addr) {
			return p.CurAddr
		}
	}
	return ""
}

// pathChanges counts the transitions between consecutive samples.
func pathChanges(samples []string) int {
	changes := 0
	for i := 1; i < len(samples); i++ {
		if samples[i] != samples[i-1] {
			changes++
		}
	}
	return changes
}

func hasAddr(list []netip.Addr, addr netip.Addr) bool {
	for _, a := range list {
		if a == addr {
			return true
		}
	}
	return false
}
