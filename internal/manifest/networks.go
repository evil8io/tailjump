package manifest

import (
	"net/netip"

	"go4.org/netipx"
)

// reserved lists the ranges a session never routes, on top of the manifest
// excludes and the laptop's own subnets.
var reserved = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"),       // tailnet
	netip.MustParsePrefix("fd7a:115c:a1e0::/48"), // tailnet
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("224.0.0.0/3"),
	netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("ff00::/8"),
}

// Inputs is everything ComputeNetworks needs. The caller parses the manifest
// and the discovery result into these fields.
type Inputs struct {
	ManifestNetworks    []netip.Prefix
	ManifestExclude     []netip.Prefix
	DiscoveryLinkRoutes []netip.Prefix
	DiscoveryCloud      []netip.Prefix
	RemoteAddrs         []netip.Addr
	LaptopConnected     []netip.Prefix
	LocalExclude        []netip.Prefix
}

// ComputeNetworks returns the minimal sorted session prefix list: the manifest
// networks plus the discovery result, minus the excludes, the reserved ranges,
// the remote's addresses, and the laptop's connected subnets.
func ComputeNetworks(in Inputs) ([]netip.Prefix, error) {
	var incl netipx.IPSetBuilder
	for _, p := range in.ManifestNetworks {
		incl.AddPrefix(p)
	}
	for _, p := range in.DiscoveryLinkRoutes {
		incl.AddPrefix(p)
	}
	for _, p := range in.DiscoveryCloud {
		incl.AddPrefix(p)
	}
	inclSet, err := incl.IPSet()
	if err != nil {
		return nil, err
	}

	var excl netipx.IPSetBuilder
	for _, p := range in.ManifestExclude {
		excl.AddPrefix(p)
	}
	for _, p := range in.LaptopConnected {
		excl.AddPrefix(p)
	}
	for _, p := range in.LocalExclude {
		excl.AddPrefix(p)
	}
	for _, p := range reserved {
		excl.AddPrefix(p)
	}
	for _, a := range in.RemoteAddrs {
		excl.Add(a)
	}
	exclSet, err := excl.IPSet()
	if err != nil {
		return nil, err
	}

	var out netipx.IPSetBuilder
	out.AddSet(inclSet)
	out.RemoveSet(exclSet)
	finalSet, err := out.IPSet()
	if err != nil {
		return nil, err
	}
	return finalSet.Prefixes(), nil
}
