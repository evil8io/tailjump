// Package protocols has the protocol set of a session: which of TCP, UDP,
// and ICMP echo the client forwards. The config, the plan, the state, and
// the data plane share it. It imports the standard library only.
package protocols

import (
	"errors"
	"fmt"
	"strings"
)

// The protocol names, in the canonical order of String.
const (
	TCP  = "tcp"
	UDP  = "udp"
	ICMP = "icmp"
)

// Set is the protocols a session forwards.
type Set struct {
	TCP  bool
	UDP  bool
	ICMP bool
}

// All is the default set: every protocol on.
func All() Set {
	return Set{TCP: true, UDP: true, ICMP: true}
}

// Parse parses a comma-separated list such as "tcp,udp,icmp". The order
// does not matter. An empty list, an unknown name, and a repeated name are
// errors.
func Parse(s string) (Set, error) {
	var set Set
	if strings.TrimSpace(s) == "" {
		return set, errors.New("protocols: empty list, want one or more of tcp, udp, icmp")
	}
	for _, name := range strings.Split(s, ",") {
		name = strings.TrimSpace(name)
		var field *bool
		switch name {
		case TCP:
			field = &set.TCP
		case UDP:
			field = &set.UDP
		case ICMP:
			field = &set.ICMP
		default:
			return Set{}, fmt.Errorf("protocols: unknown protocol %q, want tcp, udp, or icmp", name)
		}
		if *field {
			return Set{}, fmt.Errorf("protocols: %s is listed twice", name)
		}
		*field = true
	}
	return set, nil
}

// Valid reports whether s parses as a set.
func Valid(s string) bool {
	_, err := Parse(s)
	return err == nil
}

// Resolve picks the set by precedence: the flag, then the remote config,
// then the defaults, then all. Each value is a list as Parse reads it.
func Resolve(flag, remote, defaults string) (Set, error) {
	for _, s := range []string{flag, remote, defaults} {
		if s != "" {
			return Parse(s)
		}
	}
	return All(), nil
}

// String formats the set in the canonical order, for example "tcp,udp,icmp".
func (s Set) String() string {
	var names []string
	if s.TCP {
		names = append(names, TCP)
	}
	if s.UDP {
		names = append(names, UDP)
	}
	if s.ICMP {
		names = append(names, ICMP)
	}
	return strings.Join(names, ",")
}
