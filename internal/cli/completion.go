package cli

import (
	"context"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/evil8io/tailjump/internal/tailnet"
)

// remoteCompletionTimeout bounds the local API read that completeRemote and
// completeTag make, so a stuck tailscaled does not stall a shell TAB press.
const remoteCompletionTimeout = time.Second

// onlineStatus reads the tailnet status within remoteCompletionTimeout. It
// returns nil on any failure, so a completion function falls back quietly
// instead of printing an error.
func onlineStatus() *tailnet.Status {
	ctx, cancel := context.WithTimeout(context.Background(), remoteCompletionTimeout)
	defer cancel()
	st, err := newTailnetClient().Status(ctx)
	if err != nil {
		return nil
	}
	return st
}

// completeRemote completes the <remote> argument of connect, describe, and
// doctor: the config aliases, the hostnames of the online peers, and their
// tags in the form tag:x, deduplicated. A config load error yields no
// aliases; a peer read failure yields the aliases only.
func completeRemote(_ *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var names []string
	if cfg, err := loadLocalConfig(); err == nil {
		names = append(names, sortedRemoteAliases(cfg)...)
	}
	if st := onlineStatus(); st != nil {
		seenTags := map[string]bool{}
		for _, p := range st.Peers {
			if !p.Online {
				continue
			}
			names = append(names, p.HostName)
			for _, t := range p.Tags {
				if seenTags[t] {
					continue
				}
				seenTags[t] = true
				names = append(names, t)
			}
		}
	}
	return filterPrefix(names, toComplete), cobra.ShellCompDirectiveNoFileComp
}

// completeTag completes --tag of list: the tags of the online peers,
// without the tag: prefix, deduplicated.
func completeTag(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	st := onlineStatus()
	if st == nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	seen := map[string]bool{}
	var tags []string
	for _, p := range st.Peers {
		if !p.Online {
			continue
		}
		for _, t := range p.Tags {
			t = strings.TrimPrefix(t, "tag:")
			if seen[t] {
				continue
			}
			seen[t] = true
			tags = append(tags, t)
		}
	}
	return filterPrefix(tags, toComplete), cobra.ShellCompDirectiveNoFileComp
}

// completeAlias completes the <alias> argument of alias show, alias set,
// alias remove, and alias unset: the config aliases. A config load error
// yields no completions.
func completeAlias(_ *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	cfg, err := loadLocalConfig()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return filterPrefix(sortedRemoteAliases(cfg), toComplete), cobra.ShellCompDirectiveNoFileComp
}

// completeAliasUnset completes alias unset: the config aliases for the
// first argument, then aliasFields minus the fields already on the line.
func completeAliasUnset(_ *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) == 0 {
		cfg, err := loadLocalConfig()
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return filterPrefix(sortedRemoteAliases(cfg), toComplete), cobra.ShellCompDirectiveNoFileComp
	}
	used := map[string]bool{}
	for _, f := range args[1:] {
		used[f] = true
	}
	var remaining []string
	for _, f := range aliasFields {
		if !used[f] {
			remaining = append(remaining, f)
		}
	}
	return filterPrefix(remaining, toComplete), cobra.ShellCompDirectiveNoFileComp
}

// completeConfigKey completes the <key> argument of config unset: configKeys.
func completeConfigKey(_ *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return filterPrefix(configKeys, toComplete), cobra.ShellCompDirectiveNoFileComp
}

// completeConfigSetValue completes config set: configKeys for the first
// argument, then the S4 type values for defaults.dns, defaults.transport,
// and defaults.protocols.
func completeConfigSetValue(_ *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) == 0 {
		return filterPrefix(configKeys, toComplete), cobra.ShellCompDirectiveNoFileComp
	}
	if len(args) > 1 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var values []string
	switch args[0] {
	case "defaults.dns":
		values = (&dnsModeValue{}).Values()
	case "defaults.transport":
		values = (&transportModeValue{}).Values()
	case "defaults.protocols":
		values = (&protocolSetValue{}).Values()
	}
	return filterPrefix(values, toComplete), cobra.ShellCompDirectiveNoFileComp
}

// completeDNSMode completes --dns with the dnsModeValue values.
func completeDNSMode(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	return filterPrefix((&dnsModeValue{}).Values(), toComplete), cobra.ShellCompDirectiveNoFileComp
}

// completeTransportMode completes --transport with the transportModeValue
// values.
func completeTransportMode(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	return filterPrefix((&transportModeValue{}).Values(), toComplete), cobra.ShellCompDirectiveNoFileComp
}

// completeProtocols completes --protocols with the next item after the last
// comma, without the items already in the list. It returns
// ShellCompDirectiveNoSpace, so a comma can follow the completion at once.
func completeProtocols(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	prefix, last := "", toComplete
	used := map[string]bool{}
	if idx := strings.LastIndex(toComplete, ","); idx >= 0 {
		prefix, last = toComplete[:idx+1], toComplete[idx+1:]
		for _, item := range strings.Split(toComplete[:idx], ",") {
			used[item] = true
		}
	}
	var out []string
	for _, v := range (&protocolSetValue{}).Values() {
		if used[v] || !strings.HasPrefix(v, last) {
			continue
		}
		out = append(out, prefix+v)
	}
	return out, cobra.ShellCompDirectiveNoFileComp | cobra.ShellCompDirectiveNoSpace
}

// registerRemoteFlagCompletions registers the completions shared by connect
// and the tj alias commands that take the remote-config flags: --dns,
// --transport, and --protocols get the S4 type values; --user, --network,
// and --exclude get no file completion, because their values are not
// enumerable.
func registerRemoteFlagCompletions(cmd *cobra.Command) {
	_ = cmd.RegisterFlagCompletionFunc("dns", completeDNSMode)
	_ = cmd.RegisterFlagCompletionFunc("transport", completeTransportMode)
	_ = cmd.RegisterFlagCompletionFunc("protocols", completeProtocols)
	_ = cmd.RegisterFlagCompletionFunc("user", cobra.NoFileCompletions)
	_ = cmd.RegisterFlagCompletionFunc("network", cobra.NoFileCompletions)
	_ = cmd.RegisterFlagCompletionFunc("exclude", cobra.NoFileCompletions)
}

// filterPrefix returns the items that start with prefix. An empty prefix
// returns items unchanged.
func filterPrefix(items []string, prefix string) []string {
	if prefix == "" {
		return items
	}
	var out []string
	for _, it := range items {
		if strings.HasPrefix(it, prefix) {
			out = append(out, it)
		}
	}
	return out
}
