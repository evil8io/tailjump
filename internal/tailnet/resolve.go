package tailnet

import (
	"fmt"
	"strconv"
	"strings"
)

// Resolve finds the peer that ref addresses, considering online peers only:
// a remote gateway is ephemeral and deregisters on termination, so an
// offline peer never matches.
//
// ref is a tag such as "tag:example", or a hostname (already alias-resolved
// by the caller). A tag matches every online peer that carries it. A
// hostname matches an online peer whose HostName equals ref, or equals ref
// followed by a Tailscale collision suffix: a hyphen and a number, for
// example "gw-1". Tailscale adds that suffix when a replacement node joins
// before the old same-named node has deregistered.
//
// More than one online match is not an error: an exact base-name match
// wins over a suffixed one, the highest suffix number wins among suffixed
// matches, and a remaining tie (or a tie among same-tagged peers) goes to
// the most recent WireGuard handshake.
//
// Zero online matches is an error that lists the offline peers ref would
// otherwise have matched, if there are any.
func Resolve(peers []Peer, ref string) (*Peer, error) {
	if IsTagRef(ref) {
		return resolveTag(peers, ref)
	}
	return resolveHostname(peers, ref)
}

func resolveTag(peers []Peer, tag string) (*Peer, error) {
	var all, online []Peer
	for _, p := range peers {
		if !HasTag(p.Tags, tag) {
			continue
		}
		all = append(all, p)
		if p.Online {
			online = append(online, p)
		}
	}
	if len(online) == 0 {
		return nil, fmt.Errorf("no online peer has tag %q%s", tag, offlineNames(all))
	}
	best := online[0]
	for _, p := range online[1:] {
		if p.LastHandshake.After(best.LastHandshake) {
			best = p
		}
	}
	return &best, nil
}

// hostMatch is one peer whose hostname matches a resolveHostname reference,
// either exactly or with a collision suffix.
type hostMatch struct {
	peer   Peer
	exact  bool
	suffix int
}

func resolveHostname(peers []Peer, ref string) (*Peer, error) {
	var all, online []hostMatch
	for _, p := range peers {
		m, ok := matchHostname(p.HostName, ref)
		if !ok {
			continue
		}
		m.peer = p
		all = append(all, m)
		if p.Online {
			online = append(online, m)
		}
	}
	if len(online) == 0 {
		names := make([]Peer, len(all))
		for i, m := range all {
			names[i] = m.peer
		}
		return nil, fmt.Errorf("no online peer matches %q%s", ref, offlineNames(names))
	}
	best := online[0]
	for _, m := range online[1:] {
		if betterHostMatch(m, best) {
			best = m
		}
	}
	p := best.peer
	return &p, nil
}

// matchHostname reports whether hostname is ref itself, or ref with a
// collision suffix: a hyphen and a canonical (no leading zero) non-negative
// integer, for example "gw-1" for ref "gw".
func matchHostname(hostname, ref string) (hostMatch, bool) {
	if hostname == ref {
		return hostMatch{exact: true}, true
	}
	prefix := ref + "-"
	if !strings.HasPrefix(hostname, prefix) {
		return hostMatch{}, false
	}
	rest := hostname[len(prefix):]
	n, err := strconv.Atoi(rest)
	if err != nil || n < 0 || strconv.Itoa(n) != rest {
		return hostMatch{}, false
	}
	return hostMatch{suffix: n}, true
}

// betterHostMatch reports whether a should replace b as the resolved match:
// an exact match beats a suffixed one, a higher suffix beats a lower one,
// and a tie goes to the most recent WireGuard handshake.
func betterHostMatch(a, b hostMatch) bool {
	if a.exact != b.exact {
		return a.exact
	}
	if !a.exact && a.suffix != b.suffix {
		return a.suffix > b.suffix
	}
	return a.peer.LastHandshake.After(b.peer.LastHandshake)
}

func offlineNames(peers []Peer) string {
	if len(peers) == 0 {
		return ""
	}
	names := make([]string, len(peers))
	for i, p := range peers {
		names[i] = p.HostName
	}
	return fmt.Sprintf("; offline: %s", strings.Join(names, ", "))
}
