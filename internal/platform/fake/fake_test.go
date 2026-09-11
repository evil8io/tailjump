package fake_test

import (
	"errors"
	"net/netip"
	"testing"

	"github.com/evil8io/tailjump/internal/platform"
	"github.com/evil8io/tailjump/internal/platform/fake"
)

var (
	_ platform.Device   = (*fake.Device)(nil)
	_ platform.Router   = (*fake.Router)(nil)
	_ platform.Resolver = (*fake.Resolver)(nil)
	_ platform.Runner   = (*fake.Runner)(nil)
	_ platform.Paths    = (*fake.Paths)(nil)
)

func TestDevice(t *testing.T) {
	d := &fake.Device{}
	d.CreateName = "tj0"

	_, name, err := d.Create("tj0", 1500)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if name != "tj0" {
		t.Fatalf("Create name = %q, want tj0", name)
	}
	if len(d.CreateCalls) != 1 || d.CreateCalls[0] != (fake.CreateCall{Name: "tj0", MTU: 1500}) {
		t.Fatalf("CreateCalls = %+v", d.CreateCalls)
	}

	addrs := []netip.Prefix{netip.MustParsePrefix("169.254.117.1/32")}
	d.ConfigureErr = errors.New("configure boom")
	if err := d.Configure("tj0", addrs); !errors.Is(err, d.ConfigureErr) {
		t.Fatalf("Configure err = %v, want %v", err, d.ConfigureErr)
	}
	if len(d.ConfigureCalls) != 1 || d.ConfigureCalls[0].Name != "tj0" {
		t.Fatalf("ConfigureCalls = %+v", d.ConfigureCalls)
	}

	d.DeleteErr = errors.New("delete boom")
	if err := d.Delete("tj0"); !errors.Is(err, d.DeleteErr) {
		t.Fatalf("Delete err = %v, want %v", err, d.DeleteErr)
	}
	if len(d.DeleteCalls) != 1 || d.DeleteCalls[0] != "tj0" {
		t.Fatalf("DeleteCalls = %+v", d.DeleteCalls)
	}
}

func TestRouter(t *testing.T) {
	r := &fake.Router{}
	prefixes := []netip.Prefix{netip.MustParsePrefix("172.16.0.0/12")}

	r.AddErr = errors.New("add boom")
	if err := r.Add("tj0", prefixes); !errors.Is(err, r.AddErr) {
		t.Fatalf("Add err = %v, want %v", err, r.AddErr)
	}

	r.RemoveErr = errors.New("remove boom")
	if err := r.Remove("tj0", prefixes); !errors.Is(err, r.RemoveErr) {
		t.Fatalf("Remove err = %v, want %v", err, r.RemoveErr)
	}
	if len(r.AddCalls) != 1 || len(r.RemoveCalls) != 1 {
		t.Fatalf("AddCalls = %+v, RemoveCalls = %+v", r.AddCalls, r.RemoveCalls)
	}

	r.ConnectedValue = prefixes
	got, err := r.Connected()
	if err != nil {
		t.Fatalf("Connected: %v", err)
	}
	if len(got) != 1 || got[0] != prefixes[0] {
		t.Fatalf("Connected = %+v, want %+v", got, prefixes)
	}
	if r.ConnectedCalls != 1 {
		t.Fatalf("ConnectedCalls = %d, want 1", r.ConnectedCalls)
	}
}

func TestResolver(t *testing.T) {
	r := &fake.Resolver{AvailableValue: true}
	if !r.Available() {
		t.Fatal("Available() = false, want true")
	}

	servers := []netip.Addr{netip.MustParseAddr("10.1.0.2")}
	domains := []string{"corp.example"}

	r.ApplySplitErr = errors.New("split boom")
	if err := r.ApplySplit("tj0", servers, domains); !errors.Is(err, r.ApplySplitErr) {
		t.Fatalf("ApplySplit err = %v, want %v", err, r.ApplySplitErr)
	}

	r.ApplyAllErr = errors.New("all boom")
	if err := r.ApplyAll("tj0", servers, domains); !errors.Is(err, r.ApplyAllErr) {
		t.Fatalf("ApplyAll err = %v, want %v", err, r.ApplyAllErr)
	}

	r.RevertErr = errors.New("revert boom")
	if err := r.Revert("tj0"); !errors.Is(err, r.RevertErr) {
		t.Fatalf("Revert err = %v, want %v", err, r.RevertErr)
	}

	if len(r.ApplySplitCalls) != 1 || len(r.ApplyAllCalls) != 1 || len(r.RevertCalls) != 1 {
		t.Fatalf("ApplySplitCalls = %+v, ApplyAllCalls = %+v, RevertCalls = %+v",
			r.ApplySplitCalls, r.ApplyAllCalls, r.RevertCalls)
	}
}

func TestRunner(t *testing.T) {
	r := &fake.Runner{}

	r.StartErr = errors.New("start boom")
	if err := r.Start("/run/tj/plan.json"); !errors.Is(err, r.StartErr) {
		t.Fatalf("Start err = %v, want %v", err, r.StartErr)
	}

	r.StopErr = errors.New("stop boom")
	if err := r.Stop(); !errors.Is(err, r.StopErr) {
		t.Fatalf("Stop err = %v, want %v", err, r.StopErr)
	}

	r.ActiveValue = true
	active, err := r.Active()
	if err != nil {
		t.Fatalf("Active: %v", err)
	}
	if !active {
		t.Fatal("Active() = false, want true")
	}

	if len(r.StartCalls) != 1 || r.StartCalls[0] != "/run/tj/plan.json" {
		t.Fatalf("StartCalls = %+v", r.StartCalls)
	}
	if r.StopCalls != 1 {
		t.Fatalf("StopCalls = %d, want 1", r.StopCalls)
	}
	if r.ActiveCalls != 1 {
		t.Fatalf("ActiveCalls = %d, want 1", r.ActiveCalls)
	}
}

func TestPaths(t *testing.T) {
	p := &fake.Paths{
		ConfigDirValue:  "/config/tj",
		CacheDirValue:   "/cache/tj",
		RuntimeDirValue: "/run/tj",
	}

	if got := p.ConfigDir(); got != "/config/tj" {
		t.Fatalf("ConfigDir() = %q, want /config/tj", got)
	}
	if got := p.CacheDir(); got != "/cache/tj" {
		t.Fatalf("CacheDir() = %q, want /cache/tj", got)
	}
	if got := p.RuntimeDir(); got != "/run/tj" {
		t.Fatalf("RuntimeDir() = %q, want /run/tj", got)
	}
	if p.ConfigDirCalls != 1 || p.CacheDirCalls != 1 || p.RuntimeDirCalls != 1 {
		t.Fatalf("call counts = %d, %d, %d, want 1, 1, 1", p.ConfigDirCalls, p.CacheDirCalls, p.RuntimeDirCalls)
	}
}
