//go:build linux

package linux

import (
	"errors"
	"fmt"
	"net/netip"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// Router implements platform.Router for Linux over netlink.
type Router struct{}

func NewRouter() *Router { return &Router{} }

// Add installs one connected route per prefix through the device. It uses
// replace, so a repeated add is idempotent.
func (r *Router) Add(device string, prefixes []netip.Prefix) error {
	link, err := netlink.LinkByName(device)
	if err != nil {
		return fmt.Errorf("link %s: %w", device, err)
	}
	index := link.Attrs().Index
	for _, p := range prefixes {
		route := &netlink.Route{
			LinkIndex: index,
			Dst:       prefixToIPNet(p),
			Scope:     netlink.Scope(unix.RT_SCOPE_LINK),
		}
		if err := netlink.RouteReplace(route); err != nil {
			return fmt.Errorf("add route %s dev %s: %w", p, device, err)
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
