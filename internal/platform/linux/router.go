//go:build linux

package linux

import (
	"errors"
	"fmt"
	"net/netip"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// The session routes live in their own table behind an ip rule that sits
// after tailscaled's rules. tailscaled marks its own packets and sends them
// to the main table, so a session route in main would capture the WireGuard
// packets that carry the session. See docs/architecture.md, "Device and
// routes".
const (
	routeTable   = 117
	rulePriority = 5300
)

var ruleFamilies = []int{netlink.FAMILY_V4, netlink.FAMILY_V6}

// Router implements platform.Router for Linux over netlink.
type Router struct{}

func NewRouter() *Router { return &Router{} }

// Add installs the rules and one connected route per prefix through the
// device in the session table. It uses replace, so a repeated add is
// idempotent.
func (r *Router) Add(device string, prefixes []netip.Prefix) error {
	link, err := netlink.LinkByName(device)
	if err != nil {
		return fmt.Errorf("link %s: %w", device, err)
	}
	if err := ensureRules(); err != nil {
		return err
	}
	index := link.Attrs().Index
	for _, p := range prefixes {
		route := &netlink.Route{
			LinkIndex: index,
			Dst:       prefixToIPNet(p),
			Scope:     netlink.Scope(unix.RT_SCOPE_LINK),
			Table:     routeTable,
		}
		if err := netlink.RouteReplace(route); err != nil {
			return fmt.Errorf("add route %s dev %s: %w", p, device, err)
		}
	}
	return nil
}

func sessionRule(family int) *netlink.Rule {
	rule := netlink.NewRule()
	rule.Priority = rulePriority
	rule.Table = routeTable
	rule.Family = family
	return rule
}

func ruleExists(family int) (bool, error) {
	rules, err := netlink.RuleList(family)
	if err != nil {
		return false, fmt.Errorf("list rules: %w", err)
	}
	for _, rule := range rules {
		if rule.Priority == rulePriority && rule.Table == routeTable {
			return true, nil
		}
	}
	return false, nil
}

func ensureRules() error {
	for _, family := range ruleFamilies {
		exists, err := ruleExists(family)
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		if err := netlink.RuleAdd(sessionRule(family)); err != nil && !errors.Is(err, unix.EEXIST) {
			return fmt.Errorf("add rule pref %d lookup %d: %w", rulePriority, routeTable, err)
		}
	}
	return nil
}

// Reset flushes the session table and deletes the rules. A device delete
// removes the routes on its own, but not the rules.
func (r *Router) Reset() error {
	for _, family := range ruleFamilies {
		routes, err := netlink.RouteListFiltered(family, &netlink.Route{Table: routeTable}, netlink.RT_FILTER_TABLE)
		if err != nil {
			return fmt.Errorf("list table %d: %w", routeTable, err)
		}
		for i := range routes {
			if err := netlink.RouteDel(&routes[i]); err != nil && !errors.Is(err, unix.ESRCH) && !errors.Is(err, unix.ENOENT) {
				return fmt.Errorf("flush table %d: %w", routeTable, err)
			}
		}
		exists, err := ruleExists(family)
		if err != nil {
			return err
		}
		if !exists {
			continue
		}
		if err := netlink.RuleDel(sessionRule(family)); err != nil && !errors.Is(err, unix.ENOENT) {
			return fmt.Errorf("delete rule pref %d: %w", rulePriority, err)
		}
	}
	return nil
}

// Remove deletes the routes. A missing route is not an error, so cleanup is
// safe to repeat.
func (r *Router) Remove(device string, prefixes []netip.Prefix) error {
	link, err := netlink.LinkByName(device)
	if err != nil {
		var notFound netlink.LinkNotFoundError
		if errors.As(err, &notFound) {
			return nil
		}
		return fmt.Errorf("link %s: %w", device, err)
	}
	index := link.Attrs().Index
	for _, p := range prefixes {
		route := &netlink.Route{
			LinkIndex: index,
			Dst:       prefixToIPNet(p),
			Scope:     netlink.Scope(unix.RT_SCOPE_LINK),
			Table:     routeTable,
		}
		if err := netlink.RouteDel(route); err != nil && !errors.Is(err, unix.ESRCH) && !errors.Is(err, unix.ENOENT) {
			return fmt.Errorf("remove route %s dev %s: %w", p, device, err)
		}
	}
	return nil
}

// Connected returns the client's connected subnets, without the loopback and
// without the tj device, so the session network computation subtracts them.
func (r *Router) Connected() ([]netip.Prefix, error) {
	routes, err := netlink.RouteList(nil, netlink.FAMILY_ALL)
	if err != nil {
		return nil, fmt.Errorf("list routes: %w", err)
	}
	var out []netip.Prefix
	seen := make(map[string]bool)
	for _, route := range routes {
		if route.Dst == nil || route.Gw != nil {
			continue
		}
		if route.Scope != netlink.Scope(unix.RT_SCOPE_LINK) {
			continue
		}
		link, err := netlink.LinkByIndex(route.LinkIndex)
		if err != nil {
			continue
		}
		name := link.Attrs().Name
		if name == "lo" || name == deviceName {
			continue
		}
		p, ok := netip.AddrFromSlice(route.Dst.IP)
		if !ok {
			continue
		}
		ones, _ := route.Dst.Mask.Size()
		prefix := netip.PrefixFrom(p.Unmap(), ones)
		if seen[prefix.String()] {
			continue
		}
		seen[prefix.String()] = true
		out = append(out, prefix)
	}
	return out, nil
}
