package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/evil8io/tailjump/internal/config"
	"github.com/evil8io/tailjump/internal/discovery"
	"github.com/evil8io/tailjump/internal/sshc"
	"github.com/evil8io/tailjump/internal/tailnet"
)

const probeTimeout = 5 * time.Second

func newListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the online tailnet peers",
		RunE:  runList,
	}
	cmd.Flags().String("tag", "", "list only peers with this tag")
	cmd.Flags().Bool("probe", false, "open SSH to each peer and mark the ones with a manifest")
	cmd.Flags().String("user", "", "the SSH user for --probe")
	cmd.Flags().Bool("json", false, "print JSON output")
	return cmd
}

type listEntry struct {
	HostName string   `json:"hostname"`
	Tags     []string `json:"tags,omitempty"`
	Address  string   `json:"address,omitempty"`
	Manifest *bool    `json:"manifest,omitempty"`
	Error    string   `json:"error,omitempty"`
}

func runList(cmd *cobra.Command, _ []string) error {
	tag, _ := cmd.Flags().GetString("tag")
	probe, _ := cmd.Flags().GetBool("probe")
	asJSON, _ := cmd.Flags().GetBool("json")
	flagUser, _ := cmd.Flags().GetString("user")
	if tag != "" && !tailnet.IsTagRef(tag) {
		tag = "tag:" + tag
	}

	ctx := cmd.Context()
	st, err := newTailnetClient().Status(ctx)
	if err != nil {
		return err
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
		entries = append(entries, buildListEntry(ctx, p, probe, cfg, flagUser))
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
	client, err := sshc.Dial(ctx, addr, p.HostName, user, knownHostsCacheDir())
	if err != nil {
		return false, err
	}
	defer func() { _ = client.Close() }()

	res, err := discovery.Run(func(script string) ([]byte, error) {
		return client.Run("sh", []byte(script))
	})
	if err != nil {
		return false, err
	}
	return res.ManifestPath != "", nil
}

func printListTable(cmd *cobra.Command, entries []listEntry, probe bool) error {
	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
	if probe {
		_, _ = fmt.Fprintln(w, "HOSTNAME\tTAGS\tADDRESS\tMANIFEST")
	} else {
		_, _ = fmt.Fprintln(w, "HOSTNAME\tTAGS\tADDRESS")
	}
	for _, e := range entries {
		tags := strings.Join(e.Tags, ",")
		addr := e.Address
		if addr == "" {
			addr = "error: " + e.Error
		}
		if !probe {
			_, _ = fmt.Fprintf(w, "%s\t%s\t%s\n", e.HostName, tags, addr)
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
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", e.HostName, tags, addr, manifest)
	}
	return w.Flush()
}
