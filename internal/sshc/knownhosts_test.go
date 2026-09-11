package sshc

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/ssh"
)

func genKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

func TestKnownHostsStoresNewKey(t *testing.T) {
	kh := newKnownHosts(t.TempDir())
	key := genKey(t)

	if err := kh.callback("gw.example")("", nil, key); err != nil {
		t.Fatalf("callback: %v", err)
	}

	stored, err := kh.lookup("gw.example")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if stored == nil || !bytes.Equal(stored.Marshal(), key.Marshal()) {
		t.Fatal("want the stored key to match the presented key")
	}
}

func TestKnownHostsAcceptsChangedKeyWithWarning(t *testing.T) {
	dir := t.TempDir()
	kh := newKnownHosts(dir)
	first := genKey(t)
	second := genKey(t)

	if err := kh.callback("gw.example")("", nil, first); err != nil {
		t.Fatalf("callback: %v", err)
	}
	if err := kh.callback("gw.example")("", nil, second); err != nil {
		t.Fatalf("callback on a changed key must still accept the connection: %v", err)
	}

	stored, err := kh.lookup("gw.example")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if !bytes.Equal(stored.Marshal(), first.Marshal()) {
		t.Fatal("the original stored key must remain untouched")
	}
}

func TestKnownHostsIsKeyedByHostname(t *testing.T) {
	kh := newKnownHosts(t.TempDir())
	keyA := genKey(t)
	keyB := genKey(t)

	if err := kh.callback("a.example")("", nil, keyA); err != nil {
		t.Fatal(err)
	}
	if err := kh.callback("b.example")("", nil, keyB); err != nil {
		t.Fatal(err)
	}

	got, err := kh.lookup("b.example")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Marshal(), keyB.Marshal()) {
		t.Fatal("lookup for b.example must return keyB")
	}
}

func TestKnownHostsPath(t *testing.T) {
	dir := t.TempDir()
	kh := newKnownHosts(dir)
	if kh.path != filepath.Join(dir, "known_hosts") {
		t.Fatalf("path: %s", kh.path)
	}
}
