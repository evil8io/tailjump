package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/netip"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/evil8io/tailjump/internal/config"
	"github.com/evil8io/tailjump/internal/session"
	"github.com/evil8io/tailjump/internal/tailnet"
)

const (
	probeTimeout = 5 * time.Second

	// The --path pings: the first pong to a peer with a direct path can
	// arrive over DERP, so the ping repeats until a direct pong or the
	// last try.
	pathTimeout = 2 * time.Second
	pathTries   = 3
	pathSpacing = 200 * time.Millisecond
)

// Path types of a peer, as tailscale status reports them.
const (
	pathDirect = "direct"
	pathRelay  = "relay"
	pathIdle   = "idle"
)

func newListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List the online tailnet peers",
		RunE:    runList,
	}
	cmd.Flags().String("tag", "", "list only peers with this tag")
	cmd.Flags().Bool("probe", false, "open SSH to each peer and mark the ones with a manifest")
	cmd.Flags().Bool("path", false, "ping each idle peer to learn whether its path is direct or relayed")
	cmd.Flags().String("user", "", "the SSH user for --probe")
	cmd.Flags().Bool("json", false, "print JSON output")
	return cmd
}

type listEntry struct {
	HostName string    `json:"hostname"`
	Tags     []string  `json:"tags,omitempty"`
	Address  string    `json:"address,omitempty"`
	Session  string    `json:"session,omitempty"`
	Path     *pathInfo `json:"path,omitempty"`
	Manifest *bool     `json:"manifest,omitempty"`
	Error    string    `json:"error,omitempty"`
}

// pathInfo is the Tailscale path to a peer: direct, relayed through a DERP
// region, or idle when the peer has no recent traffic and no ping was sent.
type pathInfo struct {
	Type      string  `json:"type"`
	Relay     string  `json:"relay,omitempty"`
	Endpoint  string  `json:"endpoint,omitempty"`
	LatencyMS float64 `json:"latency_ms,omitempty"`
}

// pathOf reads the path from the status with the rule of tailscale status:
// a direct address means direct, recent traffic without one means relayed,
// and no recent traffic means idle.
func pathOf(p tailnet.Peer) *pathInfo {
	switch {
	case p.Active && p.CurAddr != "":
		return &pathInfo{Type: pathDirect, Endpoint: p.CurAddr}
	case p.Active:
		return &pathInfo{Type: pathRelay, Relay: p.Relay}
	default:
		return &pathInfo{Type: pathIdle, Relay: p.Relay}
	}
}

func (pi *pathInfo) String() string {
	if pi == nil {
		return "-"
	}
	s := pi.Type
	if pi.Type == pathRelay {
		s += " " + pi.Relay
	}
	if pi.LatencyMS > 0 {
		s += fmt.Sprintf(" %.0fms", pi.LatencyMS)
	}
	return s
}

// sessionOf returns the status of the active session when it runs to the
// peer address, else an empty string. The match is on the address, because
// a replacement gateway with a collision suffix has a different address.
func sessionOf(st *session.State, address string) string {
	if st != nil && address != "" && st.Addr == address {
		return st.Status
	}
	return ""
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func runList(cmd *cobra.Command, _ []string) error {
	tag, _ := cmd.Flags().GetString("tag")
	probe, _ := cmd.Flags().GetBool("probe")
	pingIdle, _ := cmd.Flags().GetBool("path")
	asJSON, _ := cmd.Flags().GetBool("json")
	flagUser, _ := cmd.Flags().GetString("user")
	if tag != "" && !tailnet.IsTagRef(tag) {
		tag = "tag:" + tag
	}

	ctx := cmd.Context()
	tc := newTailnetClient()
	st, err := tc.Status(ctx)
	if err != nil {
		return err
	}
	active, err := session.Active()
	if err != nil {
		slog.Warn("read session state", "error", err)
	}

	var cfg *config.Config
	if probe {
		cfg, err = loadLocalConfig()
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}
	}

	var entries []listEntry
	for _, p := range st.Peers {
		if !p.Online {
			continue
		}
		if tag != "" && !tailnet.HasTag(p.Tags, tag) {
			continue
		}
		entry := buildListEntry(ctx, p, probe, cfg, flagUser)
		entry.Session = sessionOf(active, entry.Address)
		entry.Path = pathOf(p)
		entries = append(entries, entry)
	}
	if pingIdle {
		pingIdlePaths(ctx, tc, entries)
	}

	if asJSON {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(entries)
	}
	return printListTable(cmd, entries, probe)
}

func buildListEntry(ctx context.Context, p tailnet.Peer, probe bool, cfg *config.Config, flagUser string) listEntry {
	entry := listEntry{HostName: p.HostName, Tags: p.Tags}

	addr, err := p.IPv4()
	if err != nil {
		entry.Error = err.Error()
		return entry
	}
	entry.Address = addr.String()

	if probe {
		has, perr := probeManifest(ctx, p, addr, cfg, flagUser)
		if perr != nil {
			entry.Error = perr.Error()
		} else {
			entry.Manifest = &has
		}
	}
	return entry
}

// probeManifest opens a short-lived SSH connection to the peer and runs
// discovery, returning whether the remote advertises a manifest.
func probeManifest(ctx context.Context, p tailnet.Peer, addr netip.Addr, cfg *config.Config, flagUser string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	user := sshUser(flagUser, "", cfg)
	client, err := dialRemote(ctx, addr, p.HostName, user)
	if err != nil {
		return false, err
	}
	defer func() { _ = client.Close() }()

	res, err := runDiscovery(client)
	if err != nil {
		return false, err
	}
	return res.ManifestPath != "", nil
}

// pingIdlePaths pings every idle peer with an address, all at once, and
// replaces its path with the pong's path. A peer that answers no ping keeps
// the idle path.
func pingIdlePaths(ctx context.Context, tc *tailnet.Client, entries []listEntry) {
	var wg sync.WaitGroup
	for i := range entries {
		e := &entries[i]
		if e.Address == "" || e.Path == nil || e.Path.Type != pathIdle {
			continue
		}
		addr, err := netip.ParseAddr(e.Address)
		if err != nil {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if pi := pingPath(ctx, tc, addr); pi != nil {
				e.Path = pi
			}
		}()
	}
	wg.Wait()
}

// pingPath repeats the disco ping until a direct pong or the last try, and
// returns the path of the last pong, or nil without any pong.
func pingPath(ctx context.Context, tc *tailnet.Client, addr netip.Addr) *pathInfo {
	ctx, cancel := context.WithTimeout(ctx, pathTimeout)
	defer cancel()
	var last *pathInfo
	for try := 0; try < pathTries; try++ {
		r, err := tc.Ping(ctx, addr)
		if err != nil {
			slog.Debug("path ping failed", "addr", addr, "error", err)
			break
		}
		last = &pathInfo{LatencyMS: float64(r.Latency) / float64(time.Millisecond)}
		if r.Direct() {
			last.Type, last.Endpoint = pathDirect, r.Endpoint
			return last
		}
		last.Type, last.Relay = pathRelay, r.DERPRegionCode
		select {
		case <-ctx.Done():
			return last
		case <-time.After(pathSpacing):
		}
	}
	return last
}

func printListTable(cmd *cobra.Command, entries []listEntry, probe bool) error {
	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
	if probe {
		_, _ = fmt.Fprintln(w, "HOSTNAME\tTAGS\tADDRESS\tSESSION\tPATH\tMANIFEST")
	} else {
		_, _ = fmt.Fprintln(w, "HOSTNAME\tTAGS\tADDRESS\tSESSION\tPATH")
	}
	for _, e := range entries {
		tags := strings.Join(e.Tags, ",")
		addr := e.Address
		if addr == "" {
			addr = "error: " + e.Error
		}
		if !probe {
			_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", e.HostName, tags, addr, orDash(e.Session), e.Path)
			continue
		}
		manifest := "-"
		switch {
		case e.Address != "" && e.Error != "":
			manifest = "error: " + e.Error
		case e.Manifest != nil && *e.Manifest:
			manifest = "yes"
		case e.Manifest != nil:
			manifest = "no"
		}
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", e.HostName, tags, addr, orDash(e.Session), e.Path, manifest)
	}
	return w.Flush()
}
