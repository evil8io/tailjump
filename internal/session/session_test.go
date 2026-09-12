package session

import (
	"net/netip"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/evil8io/tailjump/internal/platform"
	"github.com/evil8io/tailjump/internal/platform/fake"
)

func samplePlan() *Plan {
	return &Plan{
		Remote:   "gw.example",
		Addr:     "100.64.0.10",
		User:     "root",
		Networks: []string{"10.0.0.0/16", "2001:db8::/56"},
		DNS: PlanDNS{
			Mode:    "none",
			Servers: []string{"10.0.0.2"},
			Domains: []string{"corp.example"},
		},
		HelperArch: "arm64",
		Protocols:  "tcp,udp,icmp",
	}
}

func TestPlanRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := PlanPath(dir)
	plan := samplePlan()

	b, err := plan.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := writePlan(path, b); err != nil {
		t.Fatalf("write plan: %v", err)
	}
	got, err := ReadPlan(path)
	if err != nil {
		t.Fatalf("read plan: %v", err)
	}
	if !reflect.DeepEqual(got, plan) {
		t.Fatalf("round trip mismatch:\n got %+v\nwant %+v", got, plan)
	}
}

func TestStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := StatePath(dir)
	st := &State{
		Remote:    "gw.example",
		Addr:      "100.64.0.10",
		User:      "root",
		Networks:  []string{"10.0.0.0/16"},
		DNS:       PlanDNS{Mode: "none"},
		StartedAt: "2026-09-11T10:00:00Z",
		PID:       4242,
		Status:    StatusUp,
	}
	if err := writeState(path, st); err != nil {
		t.Fatalf("write state: %v", err)
	}
	got, err := ReadState(path)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if !reflect.DeepEqual(got, st) {
		t.Fatalf("round trip mismatch:\n got %+v\nwant %+v", got, st)
	}
}

func TestRoutePrefixesNoneModeSkipsDNS(t *testing.T) {
	plan := samplePlan()
	plan.DNS.Mode = "none"
	plan.DNS.Servers = []string{"172.16.0.53"}

	routes, err := routePrefixes(plan)
	if err != nil {
		t.Fatalf("route prefixes: %v", err)
	}
	if got := prefixSet(routes); got["172.16.0.53/32"] {
		t.Fatalf("none mode must not add a DNS host route, got %v", routes)
	}
	if len(routes) != 2 {
		t.Fatalf("want the two session networks, got %v", routes)
	}
}

func TestRoutePrefixesAddsDNSHostRouteOutsideNetworks(t *testing.T) {
	plan := samplePlan()
	plan.DNS.Mode = "all"
	// One server inside a session network, one outside it.
	plan.DNS.Servers = []string{"10.0.0.2", "172.16.0.53"}

	routes, err := routePrefixes(plan)
	if err != nil {
		t.Fatalf("route prefixes: %v", err)
	}
	set := prefixSet(routes)
	if set["10.0.0.2/32"] {
		t.Fatalf("a server inside a session network needs no host route, got %v", routes)
	}
	if !set["172.16.0.53/32"] {
		t.Fatalf("a server outside the session networks needs a host route, got %v", routes)
	}
}

func TestActiveSessionRefusesAndNames(t *testing.T) {
	dir := t.TempDir()
	writeSampleState(t, dir)
	plat := platform.Platform{
		Runner: &fake.Runner{ActiveValue: true},
		Paths:  &fake.Paths{RuntimeDirValue: dir},
	}

	active, ae, err := activeSession(plat)
	if err != nil {
		t.Fatalf("active session: %v", err)
	}
	if !active || ae == nil {
		t.Fatalf("want active with an error, got active=%v ae=%v", active, ae)
	}
	msg := ae.Error()
	if !strings.Contains(msg, "gw.example") || !strings.Contains(msg, "--replace") {
		t.Fatalf("error must name the remote and --replace, got %q", msg)
	}
}

func TestActiveSessionFree(t *testing.T) {
	plat := platform.Platform{
		Runner: &fake.Runner{ActiveValue: false},
		Paths:  &fake.Paths{RuntimeDirValue: t.TempDir()},
	}
	active, _, err := activeSession(plat)
	if err != nil {
		t.Fatalf("active session: %v", err)
	}
	if active {
		t.Fatal("want no active session")
	}
}

func writeSampleState(t *testing.T, dir string) {
	t.Helper()
	st := &State{
		Remote:    "gw.example",
		StartedAt: "2026-09-11T10:00:00Z",
		Status:    StatusUp,
	}
	if err := writeState(filepath.Join(dir, stateFile), st); err != nil {
		t.Fatalf("write state: %v", err)
	}
}

func prefixSet(prefixes []netip.Prefix) map[string]bool {
	set := make(map[string]bool, len(prefixes))
	for _, p := range prefixes {
		set[p.String()] = true
	}
	return set
}

func TestPlanProtocolSet(t *testing.T) {
	p := samplePlan()
	set, err := p.protocolSet()
	if err != nil || set.String() != "tcp,udp,icmp" {
		t.Fatalf("empty plan protocols = %q %v, want all", set, err)
	}
	p.Protocols = "tcp,udp"
	set, err = p.protocolSet()
	if err != nil || set.String() != "tcp,udp" {
		t.Fatalf("plan protocols tcp,udp = %q %v", set, err)
	}
	p.Protocols = "gre"
	if _, err := p.protocolSet(); err == nil {
		t.Fatal("invalid plan protocols parsed")
	}
}
