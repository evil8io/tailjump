package session

import (
	"context"
	"log/slog"
	"net/netip"
	"time"

	"github.com/evil8io/tailjump/internal/tailnet"
)

// watchInterval is how often the session reads tailscaled's endpoint for the
// remote.
const watchInterval = 30 * time.Second

// watchTransportPath logs a warning when tailscaled's current endpoint for
// the remote is inside the session networks. That endpoint would route the
// WireGuard packets that carry the session into the session itself, and the
// path then flaps between the loop and DERP. The session routes are in their
// own table for that reason; the watch reports a regression, because the rig
// cannot reproduce the loop. The returned function stops the watch.
func watchTransportPath(ctx context.Context, addr netip.Addr, networks []netip.Prefix) func() {
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		tc := tailnet.New("")
		var warned netip.Addr
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
			endpoint, inside := endpointInside(st, addr, networks)
			switch {
			case inside && endpoint != warned:
				slog.Warn("tailscaled uses an endpoint inside the session networks; the tunnel carries its own transport",
					"endpoint", endpoint, "remote", addr)
				warned = endpoint
			case !inside:
				warned = netip.Addr{}
			}
		}
	}()
	return cancel
}

// endpointInside finds the peer with the tailnet address and reports whether
// its current endpoint address is inside one of the networks.
func endpointInside(st *tailnet.Status, addr netip.Addr, networks []netip.Prefix) (netip.Addr, bool) {
	for _, p := range st.Peers {
		if !hasAddr(p.TailscaleIPs, addr) {
			continue
		}
		ap, err := netip.ParseAddrPort(p.CurAddr)
		if err != nil {
			return netip.Addr{}, false
		}
		ip := ap.Addr().Unmap()
		for _, n := range networks {
			if n.Contains(ip) {
				return ip, true
			}
		}
		return ip, false
	}
	return netip.Addr{}, false
}

func hasAddr(list []netip.Addr, addr netip.Addr) bool {
	for _, a := range list {
		if a == addr {
			return true
		}
	}
	return false
}
