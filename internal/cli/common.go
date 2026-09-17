package cli

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"os/user"
	"path/filepath"

	"github.com/evil8io/tailjump/internal/config"
	"github.com/evil8io/tailjump/internal/discovery"
	"github.com/evil8io/tailjump/internal/platform"
	"github.com/evil8io/tailjump/internal/sshc"
	"github.com/evil8io/tailjump/internal/tailnet"
)

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

// localConfigPath is $XDG_CONFIG_HOME/tj/config.yaml, the file tj reads and
// the tj alias and tj config commands write.
func localConfigPath() string {
	return filepath.Join(platform.New().Paths.ConfigDir(), "config.yaml")
}

// loadLocalConfig reads the local config. A missing file is not an error:
// config.Load returns a zero Config for it.
func loadLocalConfig() (*config.Config, error) {
	return config.Load(localConfigPath())
}

// saveLocalConfig writes cfg to path. It defaults the schema version to 1,
// so a config created by the first tj alias or tj config write is valid.
func saveLocalConfig(path string, cfg *config.Config) error {
	if cfg.Version == 0 {
		cfg.Version = 1
	}
	return config.Save(path, cfg)
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
// IPv4 tailnet address the SSH client dials. Config is the matched config
// entry, the zero value when ref is a bare hostname or tag. Ref is the host
// resolveAlias returned, before the online-peer match, so a reconnect can
// resolve it again.
type resolvedRemote struct {
	Peer   tailnet.Peer
	Addr   netip.Addr
	User   string
	Config config.RemoteConfig
	Ref    string
}

// resolveRemote expands a config alias, fetches the tailnet status, and
// resolves ref to exactly one online peer. flagUser overrides the config.
func resolveRemote(ctx context.Context, tn *tailnet.Client, cfg *config.Config, ref, flagUser string) (*resolvedRemote, error) {
	host, aliasUser := resolveAlias(cfg, ref)

	st, err := tn.Status(ctx)
	if err != nil {
		return nil, fmt.Errorf("tailnet status: %w", err)
	}
	slog.Debug("tailnet status", "peers", len(st.Peers))
	peer, err := tailnet.Resolve(st.Peers, host)
	if err != nil {
		return nil, err
	}
	addr, err := peer.IPv4()
	if err != nil {
		return nil, err
	}
	dialUser := sshUser(flagUser, aliasUser, cfg)
	slog.Debug("resolved remote", "ref", ref, "host", host, "addr", addr, "user", dialUser)
	return &resolvedRemote{
		Peer:   *peer,
		Addr:   addr,
		User:   dialUser,
		Config: cfg.Remotes[ref],
		Ref:    host,
	}, nil
}

// dialRemote opens SSH to addr, with a debug log naming the target.
func dialRemote(ctx context.Context, addr netip.Addr, hostname, user string) (*sshc.Client, error) {
	slog.Debug("ssh dial", "addr", addr, "hostname", hostname, "user", user)
	return sshc.Dial(ctx, addr, hostname, user, knownHostsCacheDir())
}

// runDiscovery runs discovery over client, with a debug log before and a
// one-line summary after.
func runDiscovery(client *sshc.Client) (*discovery.Result, error) {
	slog.Debug("running discovery")
	res, err := discovery.Run(func(script string) ([]byte, error) {
		return client.Run("sh", []byte(script))
	})
	if err != nil {
		return nil, err
	}
	cloudNets := 0
	if res.Cloud != nil {
		cloudNets = len(res.Cloud.Networks)
	}
	slog.Debug("discovery complete",
		"manifest", valueOrAbsent(res.ManifestPath),
		"exec_dir", res.ExecDir,
		"link_routes", len(res.LinkRoutes),
		"cloud", cloudNets,
		"resolvers", len(res.Resolvers),
	)
	return res, nil
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

// formatBytes prints a byte count with a binary unit, for example "310 MiB".
// A value below 10 keeps one decimal.
func formatBytes(n uint64) string {
	v, unit := float64(n), "B"
	for _, u := range []string{"KiB", "MiB", "GiB", "TiB"} {
		if v < 1024 {
			break
		}
		v, unit = v/1024, u
	}
	if v < 9.95 && unit != "B" {
		return fmt.Sprintf("%.1f %s", v, unit)
	}
	return fmt.Sprintf("%.0f %s", v, unit)
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
