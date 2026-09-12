package tailnet

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"path/filepath"
	"testing"
	"time"
)

func startFakeLocalAPI(t *testing.T, body string) string {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "tailscaled.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/localapi/v0/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Sec-Tailscale") != "localapi" {
			http.Error(w, "missing Sec-Tailscale header", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	})
	mux.HandleFunc("/localapi/v0/ping", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Sec-Tailscale") != "localapi" {
			http.Error(w, "bad ping request", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("ip") {
		case "100.64.0.2":
			_, _ = w.Write([]byte(`{"Endpoint":"[2001:db8::2]:41641","DERPRegionCode":"","LatencySeconds":0.029}`))
		case "100.64.0.3":
			_, _ = w.Write([]byte(`{"Endpoint":"","DERPRegionCode":"xyz","LatencySeconds":0.027}`))
		default:
			_, _ = w.Write([]byte(`{"Err":"no such peer"}`))
		}
	})
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return sock
}

func TestStatusParsesSelfAndPeers(t *testing.T) {
	sock := startFakeLocalAPI(t, `{
		"Self": {"HostName":"client","Online":true,"TailscaleIPs":["100.64.0.1"]},
		"Peer": {
			"nodekey:a": {"HostName":"gw","Tags":["tag:example"],"Online":true,"TailscaleIPs":["100.64.0.2","fd7a:115c:a1e0::2"]},
			"nodekey:b": {"HostName":"client2","Online":false,"TailscaleIPs":["100.64.0.3"]}
		}
	}`)

	c := New(sock)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	st, err := c.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.Self.HostName != "client" {
		t.Fatalf("self: %+v", st.Self)
	}
	if len(st.Peers) != 2 {
		t.Fatalf("want 2 peers, got %d: %+v", len(st.Peers), st.Peers)
	}

	var gw *Peer
	for i := range st.Peers {
		if st.Peers[i].HostName == "gw" {
			gw = &st.Peers[i]
		}
	}
	if gw == nil {
		t.Fatal("gw peer not found")
	}
	if !HasTag(gw.Tags, "tag:example") {
		t.Fatalf("gw tags: %v", gw.Tags)
	}
	if len(gw.TailscaleIPs) != 2 {
		t.Fatalf("gw addrs: %v", gw.TailscaleIPs)
	}
	if _, err := gw.IPv4(); err != nil {
		t.Fatalf("IPv4: %v", err)
	}
}

func TestStatusUntaggedPeerHasNilTags(t *testing.T) {
	sock := startFakeLocalAPI(t, `{
		"Self": {"HostName":"client","Online":true,"TailscaleIPs":["100.64.0.1"]},
		"Peer": {
			"nodekey:a": {"HostName":"plain","Online":true,"TailscaleIPs":["100.64.0.4"]}
		}
	}`)

	c := New(sock)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	st, err := c.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(st.Peers) != 1 || st.Peers[0].Tags != nil {
		t.Fatalf("want one untagged peer with nil tags, got %+v", st.Peers)
	}
}

func TestStatusRejectsBadAddress(t *testing.T) {
	sock := startFakeLocalAPI(t, `{
		"Self": {"HostName":"client","Online":true,"TailscaleIPs":["100.64.0.1"]},
		"Peer": {
			"nodekey:a": {"HostName":"bad","Online":true,"TailscaleIPs":["not-an-ip"]}
		}
	}`)

	c := New(sock)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := c.Status(ctx); err == nil {
		t.Fatal("want error for an invalid tailscale address")
	}
}

func TestStatusPathFields(t *testing.T) {
	sock := startFakeLocalAPI(t, `{
		"Self": {"HostName":"client","Online":true,"TailscaleIPs":["100.64.0.1"]},
		"Peer": {
			"nodekey:a": {"HostName":"direct","Online":true,"Active":true,"CurAddr":"[2001:db8::2]:41641","Relay":"xyz","TailscaleIPs":["100.64.0.2"]},
			"nodekey:b": {"HostName":"relayed","Online":true,"Active":true,"CurAddr":"","Relay":"xyz","TailscaleIPs":["100.64.0.3"]},
			"nodekey:c": {"HostName":"idle","Online":true,"Active":false,"CurAddr":"","Relay":"xyz","TailscaleIPs":["100.64.0.4"]}
		}
	}`)
	st, err := New(sock).Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	for _, p := range st.Peers {
		switch p.HostName {
		case "direct":
			if !p.Active || p.CurAddr != "[2001:db8::2]:41641" || p.Relay != "xyz" {
				t.Errorf("direct peer: %+v", p)
			}
		case "relayed":
			if !p.Active || p.CurAddr != "" || p.Relay != "xyz" {
				t.Errorf("relayed peer: %+v", p)
			}
		case "idle":
			if p.Active || p.CurAddr != "" || p.Relay != "xyz" {
				t.Errorf("idle peer: %+v", p)
			}
		}
	}
}

func TestPing(t *testing.T) {
	sock := startFakeLocalAPI(t, `{"Self":{"HostName":"client","Online":true,"TailscaleIPs":["100.64.0.1"]},"Peer":{}}`)
	c := New(sock)
	ctx := context.Background()

	r, err := c.Ping(ctx, mustAddr(t, "100.64.0.2"))
	if err != nil {
		t.Fatalf("Ping direct: %v", err)
	}
	if !r.Direct() || r.Endpoint != "[2001:db8::2]:41641" || r.Latency != 29*time.Millisecond {
		t.Errorf("direct result: %+v", r)
	}

	r, err = c.Ping(ctx, mustAddr(t, "100.64.0.3"))
	if err != nil {
		t.Fatalf("Ping relayed: %v", err)
	}
	if r.Direct() || r.DERPRegionCode != "xyz" || r.Latency != 27*time.Millisecond {
		t.Errorf("relayed result: %+v", r)
	}

	if _, err := c.Ping(ctx, mustAddr(t, "100.64.0.9")); err == nil {
		t.Error("Ping to an unknown peer returned no error")
	}
}

func mustAddr(t *testing.T, s string) netip.Addr {
	t.Helper()
	a, err := netip.ParseAddr(s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return a
}
