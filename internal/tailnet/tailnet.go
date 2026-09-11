// Package tailnet reads the tailnet peers from the local tailscaled API.
// It talks to the unix socket directly over net/http instead of importing
// tailscale.com: see docs/architecture.md, "Deviations from the spec".
package tailnet

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"sort"
	"strings"
	"time"
)

// DefaultSocket is the tailscaled local API socket path on Linux.
const DefaultSocket = "/var/run/tailscale/tailscaled.sock"

const statusURL = "http://local-tailscaled.sock/localapi/v0/status"

// Peer is one tailnet node from the local API status.
type Peer struct {
	HostName     string
	Tags         []string
	Online       bool
	TailscaleIPs []netip.Addr

	// LastHandshake is the last successful WireGuard handshake. Resolve uses
	// it only to break a tie between two online peers whose hostnames
	// collide on the same suffix number, which a real tailnet does not
	// produce; it is not a general-purpose liveness signal.
	LastHandshake time.Time
}

// HasTag reports whether tags contains tag. Tags is nil for an untagged peer.
func HasTag(tags []string, tag string) bool {
	for _, t := range tags {
		if t == tag {
			return true
		}
	}
	return false
}

// Status is the peers seen by the local tailscaled.
type Status struct {
	Self  Peer
	Peers []Peer
}

// Client reads the tailscaled local API over a unix socket.
type Client struct {
	socket string
	http   *http.Client
}

// New returns a Client that dials socket. An empty socket uses DefaultSocket.
func New(socket string) *Client {
	if socket == "" {
		socket = DefaultSocket
	}
	dialer := net.Dialer{}
	return &Client{
		socket: socket,
		http: &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return dialer.DialContext(ctx, "unix", socket)
				},
			},
		},
	}
}

type rawPeer struct {
	HostName      string
	Tags          []string
	Online        bool
	TailscaleIPs  []string
	LastHandshake time.Time
}

type rawStatus struct {
	Self *rawPeer
	Peer map[string]rawPeer
}

// Status fetches the current status from the local API.
func (c *Client) Status(ctx context.Context) (*Status, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, statusURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Sec-Tailscale", "localapi")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("dial tailscaled local API at %s: %w", c.socket, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tailscaled local API returned %s", resp.Status)
	}

	var raw rawStatus
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode tailscaled local API status: %w", err)
	}

	st := &Status{}
	if raw.Self != nil {
		p, err := raw.Self.toPeer()
		if err != nil {
			return nil, fmt.Errorf("self peer: %w", err)
		}
		st.Self = p
	}
	for key, rp := range raw.Peer {
		p, err := rp.toPeer()
		if err != nil {
			return nil, fmt.Errorf("peer %s: %w", key, err)
		}
		st.Peers = append(st.Peers, p)
	}
	sort.Slice(st.Peers, func(i, j int) bool { return st.Peers[i].HostName < st.Peers[j].HostName })
	return st, nil
}

func (rp rawPeer) toPeer() (Peer, error) {
	addrs := make([]netip.Addr, 0, len(rp.TailscaleIPs))
	for _, s := range rp.TailscaleIPs {
		a, err := netip.ParseAddr(s)
		if err != nil {
			return Peer{}, fmt.Errorf("parse tailscale address %q: %w", s, err)
		}
		addrs = append(addrs, a)
	}
	return Peer{
		HostName:      rp.HostName,
		Tags:          rp.Tags,
		Online:        rp.Online,
		TailscaleIPs:  addrs,
		LastHandshake: rp.LastHandshake,
	}, nil
}

// IPv4 returns the first IPv4 tailnet address of the peer.
func (p Peer) IPv4() (netip.Addr, error) {
	for _, a := range p.TailscaleIPs {
		if a.Is4() {
			return a, nil
		}
	}
	return netip.Addr{}, fmt.Errorf("peer %s has no IPv4 tailnet address", p.HostName)
}

// IsTagRef reports whether ref names a tag, for example "tag:example".
func IsTagRef(ref string) bool {
	return strings.HasPrefix(ref, "tag:")
}
