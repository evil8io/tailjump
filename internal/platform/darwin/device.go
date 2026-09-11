//go:build darwin

package darwin

import (
	"fmt"
	"net"
	"net/netip"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"golang.zx2c4.com/wireguard/tun"
)

var utunNamePattern = regexp.MustCompile(`^utun([0-9]+)?$`)

// Device implements platform.Device for macOS with a utun device from
// golang.zx2c4.com/wireguard/tun.
type Device struct{}

func NewDevice() *Device { return &Device{} }

// Create opens a utun device. The kernel assigns the interface index, so a
// requested name outside the "utun" or "utun<N>" shape falls back to
// "utun", which asks the kernel for the next free index.
func (d *Device) Create(name string, mtu int) (tun.Device, string, error) {
	dev, err := tun.CreateTUN(normalizeUtunName(name), mtu)
	if err != nil {
		return nil, "", fmt.Errorf("create utun device: %w", err)
	}
	createdName, err := dev.Name()
	if err != nil {
		_ = dev.Close()
		return nil, "", fmt.Errorf("read utun device name: %w", err)
	}
	return dev, createdName, nil
}

func normalizeUtunName(name string) string {
	if utunNamePattern.MatchString(name) {
		return name
	}
	return "utun"
}

// Configure assigns the addresses and brings the device up. A utun device
// is point-to-point, so every address is set as an alias with the device's
// own address as its peer; the session adds the routes for the reachable
// networks separately.
func (d *Device) Configure(name string, addrs []netip.Prefix) error {
	for _, p := range addrs {
		var args []string
		if p.Addr().Is4() {
			args = inet4AliasArgs(name, p)
		} else {
			args = inet6AliasArgs(name, p)
		}
		if err := runCommand("ifconfig", args...); err != nil {
			return fmt.Errorf("set address %s on %s: %w", p, name, err)
		}
	}
	if err := runCommand("ifconfig", name, "up"); err != nil {
		return fmt.Errorf("bring up %s: %w", name, err)
	}
	return nil
}

func inet4AliasArgs(name string, p netip.Prefix) []string {
	addr := p.Addr().String()
	mask := net.IP(net.CIDRMask(p.Bits(), 32)).String()
	return []string{name, "inet", addr, addr, "netmask", mask, "alias"}
}

func inet6AliasArgs(name string, p netip.Prefix) []string {
	return []string{name, "inet6", p.Addr().String(), "prefixlen", strconv.Itoa(p.Bits()), "alias"}
}

// Delete destroys the utun device. macOS also destroys a utun device when
// its file descriptor closes; this is the explicit fallback for a device
// the caller knows by name only.
func (d *Device) Delete(name string) error {
	if err := runCommand("ifconfig", name, "destroy"); err != nil {
		return fmt.Errorf("destroy %s: %w", name, err)
	}
	return nil
}

// runCommand runs name with args and turns a non-zero exit into an error
// that carries the command's combined output.
func runCommand(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}
