//go:build darwin

package darwin

import (
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// resolverDir is a var, not a const, so a test can point it at a temporary
// directory instead of the real /etc/resolver, which needs root to write.
var resolverDir = "/etc/resolver"

// Resolver implements platform.Resolver for macOS. The split mode writes
// one file per domain under /etc/resolver/, which mDNSResponder reads
// without a restart. The all mode points the active network service at the
// manifest servers with networksetup, and restores the service's own
// servers and search domains on revert.
type Resolver struct{}

func NewResolver() *Resolver { return &Resolver{} }

// Available reports whether networksetup is on PATH. The all mode needs it
// to read and change a network service's DNS configuration; the split mode
// needs only a writable /etc/resolver, which every macOS host has.
func (r *Resolver) Available() bool {
	_, err := exec.LookPath("networksetup")
	return err == nil
}

func (r *Resolver) ApplySplit(device string, servers []netip.Addr, domains []string) error {
	if len(domains) == 0 {
		return fmt.Errorf("split DNS needs at least one domain")
	}
	if err := os.MkdirAll(resolverDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", resolverDir, err)
	}
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", runtimeDir, err)
	}
	written := make([]string, 0, len(domains))
	for _, domain := range domains {
		path := filepath.Join(resolverDir, domain)
		if err := os.WriteFile(path, []byte(resolverFileContent(servers)), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
		written = append(written, domain)
	}
	state := splitStatePath(device)
	if err := os.WriteFile(state, []byte(strings.Join(written, "\n")+"\n"), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", state, err)
	}
	return nil
}

func resolverFileContent(servers []netip.Addr) string {
	var b strings.Builder
	for _, s := range servers {
		fmt.Fprintf(&b, "nameserver %s\n", s)
	}
	return b.String()
}

func splitStatePath(device string) string {
	return filepath.Join(runtimeDir, "resolver-"+device+".split")
}

func (r *Resolver) revertSplit(device string) error {
	state := splitStatePath(device)
	data, err := os.ReadFile(state)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", state, err)
	}
	for _, domain := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if domain == "" {
			continue
		}
		path := filepath.Join(resolverDir, domain)
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove %s: %w", path, err)
		}
	}
	if err := os.Remove(state); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", state, err)
	}
	return nil
}

func (r *Resolver) ApplyAll(device string, servers []netip.Addr, domains []string) error {
	service, err := activeNetworkService()
	if err != nil {
		return fmt.Errorf("find the active network service: %w", err)
	}
	oldDNS, err := currentDNSServers(service)
	if err != nil {
		return fmt.Errorf("read the current DNS servers of %s: %w", service, err)
	}
	oldSearch, err := currentSearchDomains(service)
	if err != nil {
		return fmt.Errorf("read the current search domains of %s: %w", service, err)
	}
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", runtimeDir, err)
	}
	if err := saveAllState(device, service, oldDNS, oldSearch); err != nil {
		return err
	}
	if err := setDNSServers(service, servers); err != nil {
		return fmt.Errorf("set the DNS servers of %s: %w", service, err)
	}
	if err := setSearchDomains(service, domains); err != nil {
		return fmt.Errorf("set the search domains of %s: %w", service, err)
	}
	return nil
}

func (r *Resolver) revertAll(device string) error {
	st, err := loadAllState(device)
	if err != nil {
		return fmt.Errorf("read the saved DNS state for %s: %w", device, err)
	}
	if st == nil {
		return nil
	}
	dnsAddrs := make([]netip.Addr, 0, len(st.dns))
	for _, s := range st.dns {
		addr, err := netip.ParseAddr(s)
		if err != nil {
			return fmt.Errorf("parse the saved DNS server %q: %w", s, err)
		}
		dnsAddrs = append(dnsAddrs, addr)
	}
	if err := setDNSServers(st.service, dnsAddrs); err != nil {
		return fmt.Errorf("restore the DNS servers of %s: %w", st.service, err)
	}
	if err := setSearchDomains(st.service, st.search); err != nil {
		return fmt.Errorf("restore the search domains of %s: %w", st.service, err)
	}
	path := allStatePath(device)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	return nil
}

// Revert removes every DNS change for the device. It runs both the split
// and the all revert paths, because a state file records which mode ran;
// with no state file, each path is a no-op, so a repeat call is safe.
func (r *Resolver) Revert(device string) error {
	if err := r.revertSplit(device); err != nil {
		return err
	}
	return r.revertAll(device)
}

func activeNetworkService() (string, error) {
	out, err := exec.Command("route", "-n", "get", "default").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("route -n get default: %w: %s", err, strings.TrimSpace(string(out)))
	}
	iface, err := parseRouteGetInterface(string(out))
	if err != nil {
		return "", err
	}
	portsOut, err := exec.Command("networksetup", "-listallhardwareports").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("networksetup -listallhardwareports: %w: %s", err, strings.TrimSpace(string(portsOut)))
	}
	service, err := serviceForInterface(string(portsOut), iface)
	if err != nil {
		return "", err
	}
	return service, nil
}

func parseRouteGetInterface(output string) (string, error) {
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "interface:" {
			return fields[1], nil
		}
	}
	return "", fmt.Errorf("no default route interface in %q", strings.TrimSpace(output))
}

// serviceForInterface reads the "networksetup -listallhardwareports"
// output, which lists one "Hardware Port" and "Device" pair per block
// separated by a blank line, and returns the hardware port name for iface.
func serviceForInterface(output, iface string) (string, error) {
	ports := parseHardwarePorts(output)
	name, ok := ports[iface]
	if !ok {
		return "", fmt.Errorf("no network service for interface %s", iface)
	}
	return name, nil
}

func parseHardwarePorts(output string) map[string]string {
	ports := make(map[string]string)
	var name string
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if v, ok := strings.CutPrefix(line, "Hardware Port: "); ok {
			name = v
			continue
		}
		if v, ok := strings.CutPrefix(line, "Device: "); ok && name != "" {
			ports[v] = name
			name = ""
		}
	}
	return ports
}

func currentDNSServers(service string) ([]netip.Addr, error) {
	out, err := exec.Command("networksetup", "-getdnsservers", service).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("networksetup -getdnsservers %s: %w: %s", service, err, strings.TrimSpace(string(out)))
	}
	return parseDNSServersOutput(string(out))
}

func parseDNSServersOutput(output string) ([]netip.Addr, error) {
	text := strings.TrimSpace(output)
	if text == "" || strings.Contains(text, "aren't any DNS Servers") {
		return nil, nil
	}
	var addrs []netip.Addr
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		addr, err := netip.ParseAddr(line)
		if err != nil {
			return nil, fmt.Errorf("parse DNS server %q: %w", line, err)
		}
		addrs = append(addrs, addr)
	}
	return addrs, nil
}

func currentSearchDomains(service string) ([]string, error) {
	out, err := exec.Command("networksetup", "-getsearchdomains", service).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("networksetup -getsearchdomains %s: %w: %s", service, err, strings.TrimSpace(string(out)))
	}
	return parseSearchDomainsOutput(string(out)), nil
}

func parseSearchDomainsOutput(output string) []string {
	text := strings.TrimSpace(output)
	if text == "" || strings.Contains(text, "aren't any Search Domains") {
		return nil
	}
	var domains []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			domains = append(domains, line)
		}
	}
	return domains
}

func setDNSServers(service string, servers []netip.Addr) error {
	args := []string{"-setdnsservers", service}
	if len(servers) == 0 {
		args = append(args, "empty")
	} else {
		for _, s := range servers {
			args = append(args, s.String())
		}
	}
	return runCommand("networksetup", args...)
}

func setSearchDomains(service string, domains []string) error {
	args := []string{"-setsearchdomains", service}
	if len(domains) == 0 {
		args = append(args, "empty")
	} else {
		args = append(args, domains...)
	}
	return runCommand("networksetup", args...)
}

type allState struct {
	service string
	dns     []string
	search  []string
}

func allStatePath(device string) string {
	return filepath.Join(runtimeDir, "resolver-"+device+".all")
}

func saveAllState(device, service string, oldDNS []netip.Addr, oldSearch []string) error {
	dnsStrs := make([]string, len(oldDNS))
	for i, a := range oldDNS {
		dnsStrs[i] = a.String()
	}
	content := service + "\n" + strings.Join(dnsStrs, ",") + "\n" + strings.Join(oldSearch, ",") + "\n"
	path := allStatePath(device)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func loadAllState(device string) (*allState, error) {
	path := allStatePath(device)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	lines := strings.SplitN(string(data), "\n", 4)
	for len(lines) < 3 {
		lines = append(lines, "")
	}
	st := &allState{service: lines[0]}
	if lines[1] != "" {
		st.dns = strings.Split(lines[1], ",")
	}
	if lines[2] != "" {
		st.search = strings.Split(lines[2], ",")
	}
	return st, nil
}
