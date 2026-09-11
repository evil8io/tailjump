package tailnet

import (
	"context"
	"net"
	"net/http"
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
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return sock
}

func TestStatusParsesSelfAndPeers(t *testing.T) {
	sock := startFakeLocalAPI(t, `{
		"Self": {"HostName":"laptop","Online":true,"TailscaleIPs":["100.64.0.1"]},
		"Peer": {
			"nodekey:a": {"HostName":"gw","Tags":["tag:example"],"Online":true,"TailscaleIPs":["100.64.0.2","fd7a:115c:a1e0::2"]},
			"nodekey:b": {"HostName":"laptop2","Online":false,"TailscaleIPs":["100.64.0.3"]}
		}
	}`)

	c := New(sock)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	st, err := c.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.Self.HostName != "laptop" {
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
		"Self": {"HostName":"laptop","Online":true,"TailscaleIPs":["100.64.0.1"]},
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
		"Self": {"HostName":"laptop","Online":true,"TailscaleIPs":["100.64.0.1"]},
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
