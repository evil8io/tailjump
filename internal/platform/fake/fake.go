// Package fake has an in-memory implementation of the tj platform
// interfaces for tests. Every call is recorded, and a test sets an error
// per method before exercising the code under test.
package fake

import (
	"net/netip"

	"golang.zx2c4.com/wireguard/tun"
)

// Device is a fake platform.Device.
type Device struct {
	CreateCalls    []CreateCall
	ConfigureCalls []ConfigureCall
	DeleteCalls    []string

	CreateErr    error
	ConfigureErr error
	DeleteErr    error

	CreateDevice tun.Device
	CreateName   string
}

type CreateCall struct {
	Name string
	MTU  int
}

type ConfigureCall struct {
	Name  string
	Addrs []netip.Prefix
}

func (d *Device) Create(name string, mtu int) (tun.Device, string, error) {
	d.CreateCalls = append(d.CreateCalls, CreateCall{Name: name, MTU: mtu})
	if d.CreateErr != nil {
		return nil, "", d.CreateErr
	}
	return d.CreateDevice, d.CreateName, nil
}

func (d *Device) Configure(name string, addrs []netip.Prefix) error {
	d.ConfigureCalls = append(d.ConfigureCalls, ConfigureCall{Name: name, Addrs: addrs})
	return d.ConfigureErr
}

func (d *Device) Delete(name string) error {
	d.DeleteCalls = append(d.DeleteCalls, name)
	return d.DeleteErr
}

// Router is a fake platform.Router.
type Router struct {
	AddCalls    []RouteCall
	RemoveCalls []RouteCall

	AddErr    error
	RemoveErr error

	ConnectedCalls int
	ConnectedValue []netip.Prefix
	ConnectedErr   error
}

type RouteCall struct {
	Device   string
	Prefixes []netip.Prefix
}

func (r *Router) Add(device string, prefixes []netip.Prefix) error {
	r.AddCalls = append(r.AddCalls, RouteCall{Device: device, Prefixes: prefixes})
	return r.AddErr
}

func (r *Router) Remove(device string, prefixes []netip.Prefix) error {
	r.RemoveCalls = append(r.RemoveCalls, RouteCall{Device: device, Prefixes: prefixes})
	return r.RemoveErr
}

func (r *Router) Connected() ([]netip.Prefix, error) {
	r.ConnectedCalls++
	return r.ConnectedValue, r.ConnectedErr
}

// Resolver is a fake platform.Resolver.
type Resolver struct {
	AvailableValue bool

	ApplySplitCalls []ApplyCall
	ApplyAllCalls   []ApplyCall
	RevertCalls     []string

	ApplySplitErr error
	ApplyAllErr   error
	RevertErr     error
}

type ApplyCall struct {
	Device  string
	Servers []netip.Addr
	Domains []string
}

func (r *Resolver) Available() bool {
	return r.AvailableValue
}

func (r *Resolver) ApplySplit(device string, servers []netip.Addr, domains []string) error {
	r.ApplySplitCalls = append(r.ApplySplitCalls, ApplyCall{Device: device, Servers: servers, Domains: domains})
	return r.ApplySplitErr
}

func (r *Resolver) ApplyAll(device string, servers []netip.Addr, domains []string) error {
	r.ApplyAllCalls = append(r.ApplyAllCalls, ApplyCall{Device: device, Servers: servers, Domains: domains})
	return r.ApplyAllErr
}

func (r *Resolver) Revert(device string) error {
	r.RevertCalls = append(r.RevertCalls, device)
	return r.RevertErr
}

// Runner is a fake platform.Runner.
type Runner struct {
	StartCalls  []string
	StopCalls   int
	ActiveCalls int

	StartErr    error
	StopErr     error
	ActiveErr   error
	ActiveValue bool
}

func (r *Runner) Start(plan string) error {
	r.StartCalls = append(r.StartCalls, plan)
	return r.StartErr
}

func (r *Runner) Stop() error {
	r.StopCalls++
	return r.StopErr
}

func (r *Runner) Active() (bool, error) {
	r.ActiveCalls++
	return r.ActiveValue, r.ActiveErr
}

// Paths is a fake platform.Paths.
type Paths struct {
	ConfigDirCalls  int
	CacheDirCalls   int
	RuntimeDirCalls int

	ConfigDirValue  string
	CacheDirValue   string
	RuntimeDirValue string
}

func (p *Paths) ConfigDir() string {
	p.ConfigDirCalls++
	return p.ConfigDirValue
}

func (p *Paths) CacheDir() string {
	p.CacheDirCalls++
	return p.CacheDirValue
}

func (p *Paths) RuntimeDir() string {
	p.RuntimeDirCalls++
	return p.RuntimeDirValue
}
