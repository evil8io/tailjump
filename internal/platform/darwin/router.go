//go:build darwin

package darwin

import (
	"bufio"
	"fmt"
	"math/bits"
	"net/netip"
	"os/exec"
	"strconv"
	"strings"
)

// Router implements platform.Router for macOS with the route command.
type Router struct{}

func NewRouter() *Router { return &Router{} }

func (r *Router) Add(device string, prefixes []netip.Prefix) error {
	return r.change("add", device, prefixes)
}

func (r *Router) Remove(device string, prefixes []netip.Prefix) error {
	return r.change("delete", device, prefixes)
}

// Reset is a no-op: the routes are in the main table, and a utun delete
// removes them. tailscaled on macOS binds its sockets to the default
// interface, so the main table cannot capture its packets.
func (r *Router) Reset() error { return nil }

func (r *Router) change(verb, device string, prefixes []netip.Prefix) error {
	for _, p := range prefixes {
		if err := runCommand("route", routeArgs(verb, device, p)...); err != nil {
			return fmt.Errorf("%s route %s on %s: %w", verb, p, device, err)
		}
	}
	return nil
}

func routeArgs(verb, device string, p netip.Prefix) []string {
	family := "-inet"
	if p.Addr().Is6() {
		family = "-inet6"
	}
	return []string{"-q", "-n", verb, family, p.String(), "-interface", device}
}

// Connected returns the connected subnets of the client from the addresses
// that ifconfig reports, without the loopback interfaces and without a
// point-to-point interface such as the tj device itself: a utun address has
// no broadcast segment, so a point-to-point alias is never a shared subnet.
func (r *Router) Connected() ([]netip.Prefix, error) {
	out, err := exec.Command("ifconfig", "-a").CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("ifconfig -a: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return parseConnected(string(out))
}

func parseConnected(output string) ([]netip.Prefix, error) {
	var prefixes []netip.Prefix
	var iface string
	var skip bool
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := scanner.Text()
		if len(line) > 0 && line[0] != ' ' && line[0] != '\t' {
			name, _, ok := strings.Cut(line, ":")
			if !ok {
				continue
			}
			iface = name
			skip = strings.HasPrefix(iface, "lo")
			continue
		}
		if skip {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		var (
			p  netip.Prefix
			ok bool
			e  error
		)
		switch fields[0] {
		case "inet":
			p, ok, e = parseInet(fields)
		case "inet6":
			p, ok, e = parseInet6(fields)
		default:
			continue
		}
		if e != nil {
			return nil, fmt.Errorf("interface %s: %w", iface, e)
		}
		if ok {
			prefixes = append(prefixes, p)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return prefixes, nil
}

// parseInet reads an "inet <addr> netmask <hex> [broadcast <addr>]" line.
// It reports ok=false for a point-to-point alias, "inet <addr> --> <dest>
// netmask <hex>".
func parseInet(fields []string) (netip.Prefix, bool, error) {
	if len(fields) < 2 {
		return netip.Prefix{}, false, fmt.Errorf("malformed inet line: %q", strings.Join(fields, " "))
	}
	if len(fields) >= 3 && fields[2] == "-->" {
		return netip.Prefix{}, false, nil
	}
	addr, err := netip.ParseAddr(fields[1])
	if err != nil {
		return netip.Prefix{}, false, fmt.Errorf("parse address %q: %w", fields[1], err)
	}
	idx := indexOf(fields, "netmask")
	if idx < 0 || idx+1 >= len(fields) {
		return netip.Prefix{}, false, fmt.Errorf("no netmask in %q", strings.Join(fields, " "))
	}
	prefixBits, err := hexMaskBits(fields[idx+1])
	if err != nil {
		return netip.Prefix{}, false, err
	}
	return netip.PrefixFrom(addr, prefixBits).Masked(), true, nil
}

// parseInet6 reads an "inet6 <addr>[%zone] prefixlen <n> ..." line. It
// reports ok=false for a point-to-point alias, the same way parseInet does.
func parseInet6(fields []string) (netip.Prefix, bool, error) {
	if len(fields) < 2 {
		return netip.Prefix{}, false, fmt.Errorf("malformed inet6 line: %q", strings.Join(fields, " "))
	}
	if len(fields) >= 3 && fields[2] == "-->" {
		return netip.Prefix{}, false, nil
	}
	addrStr, _, _ := strings.Cut(fields[1], "%")
	addr, err := netip.ParseAddr(addrStr)
	if err != nil {
		return netip.Prefix{}, false, fmt.Errorf("parse address %q: %w", fields[1], err)
	}
	idx := indexOf(fields, "prefixlen")
	if idx < 0 || idx+1 >= len(fields) {
		return netip.Prefix{}, false, fmt.Errorf("no prefixlen in %q", strings.Join(fields, " "))
	}
	prefixBits, err := strconv.Atoi(fields[idx+1])
	if err != nil {
		return netip.Prefix{}, false, fmt.Errorf("parse prefixlen %q: %w", fields[idx+1], err)
	}
	return netip.PrefixFrom(addr, prefixBits).Masked(), true, nil
}

func hexMaskBits(hex string) (int, error) {
	hex = strings.TrimPrefix(hex, "0x")
	v, err := strconv.ParseUint(hex, 16, 32)
	if err != nil {
		return 0, fmt.Errorf("parse netmask %q: %w", hex, err)
	}
	return bits.OnesCount32(uint32(v)), nil
}

func indexOf(fields []string, target string) int {
	for i, f := range fields {
		if f == target {
			return i
		}
	}
	return -1
}
