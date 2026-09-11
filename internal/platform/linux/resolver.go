//go:build linux

package linux

import (
	"errors"
	"fmt"
	"net/netip"
	"os/exec"
	"strings"
)

// commandRunner runs a command and returns its combined output. Tests inject
// a fake runner so the resolvectl argument construction is verifiable
// without a real systemd-resolved.
type commandRunner func(name string, args ...string) ([]byte, error)

func execCommand(name string, args ...string) ([]byte, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

// Resolver implements platform.Resolver for Linux. It drives
// systemd-resolved through resolvectl on the tj device, and falls back to
// rewriting /etc/resolv.conf for ApplyAll when resolved is not available.
type Resolver struct {
	run commandRunner

	resolvConfPath string
	linkFile       string
	backupFile     string
}

func NewResolver() *Resolver {
	runtimeDir := NewPaths().RuntimeDir()
	return &Resolver{
		run:            execCommand,
		resolvConfPath: "/etc/resolv.conf",
		linkFile:       runtimeDir + "/resolv.conf.link",
		backupFile:     runtimeDir + "/resolv.conf.backup",
	}
}

// Available reports whether systemd-resolved runs, by the exit code of
// resolvectl status.
func (r *Resolver) Available() bool {
	_, err := r.run("resolvectl", "status")
	return err == nil
}

// ApplySplit sends the domains to the manifest servers and leaves every
// other query on the link's own default route. It needs systemd-resolved,
// because the resolv.conf fallback has no way to route only some domains
// remotely.
func (r *Resolver) ApplySplit(device string, servers []netip.Addr, domains []string) error {
	if len(domains) == 0 {
		return errors.New("dns.domains is required for split DNS")
	}
	if !r.Available() {
		return errors.New("split DNS needs systemd-resolved, which is not available")
	}
	if err := r.setDNS(device, servers); err != nil {
		return err
	}
	if err := r.setDomains(device, routingDomains(domains)); err != nil {
		return err
	}
	return r.setDefaultRoute(device, false)
}

// ApplyAll sends every query to the manifest servers. With systemd-resolved
// it sets the device as the default route; without it, it rewrites
// /etc/resolv.conf.
func (r *Resolver) ApplyAll(device string, servers []netip.Addr, domains []string) error {
	if !r.Available() {
		return r.applyResolvConfFallback(servers, domains)
	}
	if err := r.setDNS(device, servers); err != nil {
		return err
	}
	if err := r.setDomains(device, allDomains(domains)); err != nil {
		return err
	}
	return r.setDefaultRoute(device, true)
}

// Revert removes every DNS change: the resolvectl overrides on device, and a
// resolv.conf fallback, if either was applied. It is safe to call twice.
func (r *Resolver) Revert(device string) error {
	var errs []error
	if r.Available() {
		if _, err := r.run("resolvectl", "revert", device); err != nil {
			errs = append(errs, err)
		}
	}
	if err := r.revertResolvConfFallback(); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func (r *Resolver) setDNS(device string, servers []netip.Addr) error {
	_, err := r.run("resolvectl", append([]string{"dns", device}, addrStrings(servers)...)...)
	return err
}

func (r *Resolver) setDomains(device string, domains []string) error {
	_, err := r.run("resolvectl", append([]string{"domain", device}, domains...)...)
	return err
}

func (r *Resolver) setDefaultRoute(device string, on bool) error {
	_, err := r.run("resolvectl", "default-route", device, boolArg(on))
	return err
}

func addrStrings(addrs []netip.Addr) []string {
	out := make([]string, len(addrs))
	for i, a := range addrs {
		out[i] = a.String()
	}
	return out
}

// routingDomains prefixes every domain with ~, so resolved routes queries
// for it without using it as a search suffix.
func routingDomains(domains []string) []string {
	out := make([]string, len(domains))
	for i, d := range domains {
		out[i] = "~" + d
	}
	return out
}

// allDomains is ~. plus the plain domains, so resolved routes every query
// and still offers the domains for search-suffix completion.
func allDomains(domains []string) []string {
	return append([]string{"~."}, domains...)
}

func boolArg(v bool) string {
	if v {
		return "true"
	}
	return "false"
}
