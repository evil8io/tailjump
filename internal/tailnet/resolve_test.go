package tailnet

import (
	"strings"
	"testing"
	"time"
)

func peer(hostname string, online bool, tags ...string) Peer {
	return Peer{HostName: hostname, Online: online, Tags: tags}
}

func TestResolveHostnameExactWinsWhenAllOnline(t *testing.T) {
	peers := []Peer{
		peer("gw", true),
		peer("gw-1", true),
		peer("gw-2", true),
	}
	got, err := Resolve(peers, "gw")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.HostName != "gw" {
		t.Fatalf("want gw, got %s", got.HostName)
	}
}

func TestResolveHostnameHighestSuffixWinsWithoutBaseName(t *testing.T) {
	peers := []Peer{
		peer("gw-1", true),
		peer("gw-2", true),
	}
	got, err := Resolve(peers, "gw")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.HostName != "gw-2" {
		t.Fatalf("want gw-2, got %s", got.HostName)
	}
}

func TestResolveIgnoresOfflinePeer(t *testing.T) {
	peers := []Peer{
		peer("solo", false),
	}
	if _, err := Resolve(peers, "solo"); err == nil {
		t.Fatal("want error, an offline peer must never match")
	}
}

func TestResolveOfflineExactDoesNotBeatOnlineSuffix(t *testing.T) {
	peers := []Peer{
		peer("gw", false),
		peer("gw-1", true),
	}
	got, err := Resolve(peers, "gw")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.HostName != "gw-1" {
		t.Fatalf("want gw-1, an offline exact match must not win, got %s", got.HostName)
	}
}

func TestResolveHostnameErrorListsOfflineCandidates(t *testing.T) {
	peers := []Peer{
		peer("gw", false),
		peer("gw-1", false),
	}
	_, err := Resolve(peers, "gw")
	if err == nil {
		t.Fatal("want error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "gw") || !strings.Contains(msg, "gw-1") {
		t.Fatalf("error must list offline candidates, got %q", msg)
	}
}

func TestResolveNonSuffixHostnameDoesNotMatch(t *testing.T) {
	peers := []Peer{
		peer("gateway", true),
	}
	if _, err := Resolve(peers, "gw"); err == nil {
		t.Fatal("want error, gateway must not match ref gw")
	}
}

func TestResolveTagMatchesOnlineOnly(t *testing.T) {
	peers := []Peer{
		peer("a", true, "tag:example"),
		peer("b", false, "tag:example"),
	}
	got, err := Resolve(peers, "tag:example")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.HostName != "a" {
		t.Fatalf("want a, got %s", got.HostName)
	}
}

func TestResolveTagPicksNewestHandshakeAmongSeveral(t *testing.T) {
	older := peer("a", true, "tag:example")
	older.LastHandshake = time.Now().Add(-time.Hour)
	newer := peer("b", true, "tag:example")
	newer.LastHandshake = time.Now()

	got, err := Resolve([]Peer{older, newer}, "tag:example")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.HostName != "b" {
		t.Fatalf("want b, the peer with the most recent handshake, got %s", got.HostName)
	}
}

func TestResolveTagNoOnlineMatchIsError(t *testing.T) {
	peers := []Peer{peer("a", false, "tag:example")}
	if _, err := Resolve(peers, "tag:example"); err == nil {
		t.Fatal("want error")
	}
}

func TestResolveHostnameSuffixTieBreaksOnHandshake(t *testing.T) {
	a := peer("gw-1", true)
	a.LastHandshake = time.Now().Add(-time.Minute)
	b := peer("gw-1", true)
	b.LastHandshake = time.Now()

	got, err := Resolve([]Peer{a, b}, "gw")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.LastHandshake != b.LastHandshake {
		t.Fatal("want the peer with the most recent handshake")
	}
}
