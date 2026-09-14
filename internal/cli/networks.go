package cli

import (
	"fmt"
	"net/netip"

	"github.com/evil8io/tailjump/internal/config"
	"github.com/evil8io/tailjump/internal/discovery"
	"github.com/evil8io/tailjump/internal/manifest"
)

// decodeManifest parses body into a manifest, or returns the empty manifest
// when body is empty. connect, describe, and doctor each get body from
// discovery.Result.DecodedManifest or from discovery.FetchManifest. They then
// share this decode step.
func decodeManifest(body []byte) (*manifest.Manifest, error) {
	if len(body) == 0 {
		return manifest.Empty(), nil
	}
	return manifest.Parse(body)
}

// sessionNetworks computes the session networks from the manifest, the
// discovery result, the local config, the resolved remote, and the extra
// --network and --exclude flags. A nil res means discovery did not run, or
// --no-discovery set its networks aside. connect, describe, and doctor share
// this function, so they report the same session networks for the same
// remote. It also returns the exclusion lists that describe prints. See
// docs/architecture.md, "Session networks".
func sessionNetworks(m *manifest.Manifest, res *discovery.Result, cfg *config.Config, rr *resolvedRemote, networkFlags, excludeFlags []string) ([]netip.Prefix, describeExclusions, error) {
	includeNetworks := append(append([]string{}, m.Networks...), rr.Config.Networks...)
	includeNetworks = append(includeNetworks, networkFlags...)
	manifestNetworks, err := manifest.ParsePrefixes(includeNetworks)
	if err != nil {
		return nil, describeExclusions{}, fmt.Errorf("networks: %w", err)
	}
	manifestExclude, err := manifest.ParsePrefixes(m.Exclude)
	if err != nil {
		return nil, describeExclusions{}, fmt.Errorf("manifest exclude: %w", err)
	}
	localExcludeList := append(append([]string{}, cfg.Exclude...), rr.Config.Exclude...)
	localExcludeList = append(localExcludeList, excludeFlags...)
	localExclude, err := manifest.ParsePrefixes(localExcludeList)
	if err != nil {
		return nil, describeExclusions{}, fmt.Errorf("exclude: %w", err)
	}

	var linkRoutes, cloudNets []netip.Prefix
	if res != nil {
		if m.LinkRoutesEnabled() {
			if linkRoutes, err = res.LinkRoutePrefixes(); err != nil {
				return nil, describeExclusions{}, fmt.Errorf("discovery link routes: %w", err)
			}
		}
		if m.CloudEnabled() {
			if cloudNets, err = res.CloudNetworkPrefixes(); err != nil {
				return nil, describeExclusions{}, fmt.Errorf("discovery cloud networks: %w", err)
			}
		}
	}

	connected := clientConnected()
	networks, err := manifest.ComputeNetworks(manifest.Inputs{
		ManifestNetworks:    manifestNetworks,
		ManifestExclude:     manifestExclude,
		DiscoveryLinkRoutes: linkRoutes,
		DiscoveryCloud:      cloudNets,
		RemoteAddrs:         rr.Peer.TailscaleIPs,
		ClientConnected:     connected,
		LocalExclude:        localExclude,
	})
	if err != nil {
		return nil, describeExclusions{}, fmt.Errorf("compute session networks: %w", err)
	}

	return networks, describeExclusions{
		ManifestExclude: m.Exclude,
		Reserved:        reservedForDisplay,
		RemoteAddrs:     addrStrings(rr.Peer.TailscaleIPs),
		ClientConnected: prefixStrings(connected),
		LocalExclude:    localExcludeList,
	}, nil
}
