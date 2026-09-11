//go:build linux

package linux

import (
	"errors"
	"fmt"
	"net"
	"net/netip"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	"golang.zx2c4.com/wireguard/tun"
)

// deviceName is the tj TUN device name. Router.Connected excludes it from the
// client's connected subnets.
const deviceName = "tj0"

// Device implements platform.Device for Linux. It creates the TUN through the
// wireguard tun package and configures the address, MTU, and link state over
// netlink.
type Device struct{}

func NewDevice() *Device { return &Device{} }

// Create creates the TUN device and returns the wireguard device and the real
// name the kernel assigned.
func (d *Device) Create(name string, mtu int) (tun.Device, string, error) {
	dev, err := tun.CreateTUN(name, mtu)
	if err != nil {
		return nil, "", fmt.Errorf("create tun %s: %w", name, err)
	}
	realName, err := dev.Name()
	if err != nil {
		_ = dev.Close()
		return nil, "", fmt.Errorf("read tun name: %w", err)
	}
	return dev, realName, nil
}

// Configure adds the addresses and brings the device up. It sets NODAD on
// IPv6 addresses so the global ULA and the link-local address are usable at
// once, without a duplicate-address-detection delay.
func (d *Device) Configure(name string, addrs []netip.Prefix) error {
	link, err := netlink.LinkByName(name)
	if err != nil {
		return fmt.Errorf("link %s: %w", name, err)
	}
	for _, p := range addrs {
		addr := &netlink.Addr{IPNet: prefixToIPNet(p)}
		if p.Addr().Is6() {
			addr.Flags |= unix.IFA_F_NODAD
		}
		if err := netlink.AddrAdd(link, addr); err != nil && !errors.Is(err, unix.EEXIST) {
			return fmt.Errorf("add address %s to %s: %w", p, name, err)
		}
	}
	if err := netlink.LinkSetUp(link); err != nil {
		return fmt.Errorf("bring %s up: %w", name, err)
	}
	return nil
}

// Delete removes the device. A missing device is not an error, so cleanup is
// safe to repeat.
func (d *Device) Delete(name string) error {
	link, err := netlink.LinkByName(name)
	if err != nil {
		var notFound netlink.LinkNotFoundError
		if errors.As(err, &notFound) {
			return nil
		}
		return fmt.Errorf("link %s: %w", name, err)
	}
	if err := netlink.LinkDel(link); err != nil {
		return fmt.Errorf("delete %s: %w", name, err)
	}
	return nil
}

func prefixToIPNet(p netip.Prefix) *net.IPNet {
	return &net.IPNet{
		IP:   p.Addr().AsSlice(),
		Mask: net.CIDRMask(p.Bits(), p.Addr().BitLen()),
	}
}
