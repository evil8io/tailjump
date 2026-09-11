package cli

import (
	"bytes"
	"context"
	"fmt"
	"net/netip"
	"os/user"
	"path/filepath"

	"github.com/evil8io/tailjump/internal/config"
	"github.com/evil8io/tailjump/internal/platform"
	"github.com/evil8io/tailjump/internal/sshc"
	"github.com/evil8io/tailjump/internal/tailnet"
)

// manifestOnlyScript reads the manifest without running full discovery, for
// describe --no-discovery. It mirrors the manifest lookup in
// internal/discovery/discover.sh: $XDG_CONFIG_HOME/tj/manifest.yaml (or
// $HOME/.config when that is unset), then /etc/tj/manifest.yaml. The first
// line of its output is the path that matched, empty when neither did; the
// rest is the raw manifest file.
const manifestOnlyScript = `set -eu
config_home="${XDG_CONFIG_HOME:-${HOME:-}/.config}"
for candidate in "$config_home/tj/manifest.yaml" /etc/tj/manifest.yaml; do
	if [ -r "$candidate" ]; then
		printf '%s\n' "$candidate"
		cat "$candidate"
		exit 0
	fi
done
printf '\n'
`

// reserved lists the ranges a session never routes, on top of the manifest
// excludes and the client's own subnets. It mirrors internal/manifest's
// unexported reserved list, for display only: describe and doctor show why
// a range disappeared from the session networks.
var reservedForDisplay = []string{
	"100.64.0.0/10 (tailnet)",
	"fd7a:115c:a1e0::/48 (tailnet)",
	"0.0.0.0/8",
	"127.0.0.0/8",
	"169.254.0.0/16",
	"224.0.0.0/3",
	"::1/128",
	"fe80::/10",
	"ff00::/8",
}

func newTailnetClient() *tailnet.Client {
	return tailnet.New("")
}

// knownHostsCacheDir is the directory sshc.Dial uses to store host keys.
func knownHostsCacheDir() string {
	return platform.New().Paths.CacheDir()
}

// loadLocalConfig reads $XDG_CONFIG_HOME/tj/config.yaml. A missing file is
// not an error: config.Load returns a zero Config for it.
func loadLocalConfig() (*config.Config, error) {
	dir := platform.New().Paths.ConfigDir()
	return config.Load(filepath.Join(dir, "config.yaml"))
}

// resolveAlias expands a config alias to its host and its preferred SSH
// user, if the config has an entry for ref. Otherwise ref is itself a
// hostname or a tag, and there is no per-remote user.
func resolveAlias(cfg *config.Config, ref string) (host, remoteUser string) {
	if rc, ok := cfg.Remotes[ref]; ok && rc.Host != "" {
		return rc.Host, rc.User
	}
	return ref, ""
}

// sshUser picks the SSH user: the flag, then the remote config entry, then
// the config defaults, then the local username. See docs/architecture.md,
// "Config file".
func sshUser(flagUser, remoteUser string, cfg *config.Config) string {
	if flagUser != "" {
		return flagUser
	}
	if remoteUser != "" {
		return remoteUser
	}
	if cfg.Defaults.User != "" {
		return cfg.Defaults.User
	}
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return "root"
}

// resolvedRemote is one online peer resolved from a CLI argument, with the
// IPv4 tailnet address the SSH client dials.
type resolvedRemote struct {
	Peer tailnet.Peer
	Addr netip.Addr
	User string
}

// resolveRemote expands a config alias, fetches the tailnet status, and
// resolves ref to exactly one online peer. flagUser overrides the config.
func resolveRemote(ctx context.Context, tn *tailnet.Client, cfg *config.Config, ref, flagUser string) (*resolvedRemote, error) {
	host, aliasUser := resolveAlias(cfg, ref)

	st, err := tn.Status(ctx)
	if err != nil {
		return nil, fmt.Errorf("tailnet status: %w", err)
	}
	peer, err := tailnet.Resolve(st.Peers, host)
	if err != nil {
		return nil, err
	}
	addr, err := peer.IPv4()
	if err != nil {
		return nil, err
	}
	return &resolvedRemote{
		Peer: *peer,
		Addr: addr,
		User: sshUser(flagUser, aliasUser, cfg),
	}, nil
}

// fetchManifestOnly runs manifestOnlyScript over client and splits its
// output into the manifest path (empty when the remote has none) and the
// raw manifest bytes.
func fetchManifestOnly(client *sshc.Client) (path string, content []byte, err error) {
	out, err := client.Run("sh", []byte(manifestOnlyScript))
	if err != nil {
		return "", nil, fmt.Errorf("read manifest: %w", err)
	}
	idx := bytes.IndexByte(out, '\n')
	if idx < 0 {
		return "", nil, fmt.Errorf("read manifest: unexpected output %q", out)
	}
	path = string(out[:idx])
	if path == "" {
		return "", nil, nil
	}
	return path, out[idx+1:], nil
}

func prefixStrings(prefixes []netip.Prefix) []string {
	out := make([]string, len(prefixes))
	for i, p := range prefixes {
		out[i] = p.String()
	}
	return out
}

func addrStrings(addrs []netip.Addr) []string {
	out := make([]string, len(addrs))
	for i, a := range addrs {
		out[i] = a.String()
	}
	return out
}

// clientConnected returns the client's connected subnets. Router.Connected
// is not implemented yet in this chunk (a later chunk fills in the
// platform Router), so a "not implemented" error is not fatal here: the
// session network computation proceeds without that exclusion.
func clientConnected() []netip.Prefix {
	prefixes, err := platform.New().Router.Connected()
	if err != nil {
		return nil
	}
	return prefixes
}
