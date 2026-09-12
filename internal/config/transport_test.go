package config

import (
	"path/filepath"
	"testing"
)

func TestTransportKeysRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	body := "version: 1\ndefaults:\n  transport: ssh\nremotes:\n  evil8:\n    host: gw\n    transport: quic\n"
	if err := writeFile(p, body); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Defaults.Transport != "ssh" || c.Remotes["evil8"].Transport != "quic" {
		t.Fatalf("transport keys: defaults=%q remote=%q", c.Defaults.Transport, c.Remotes["evil8"].Transport)
	}
	out := filepath.Join(dir, "out.yaml")
	if err := Save(out, c); err != nil {
		t.Fatalf("Save: %v", err)
	}
	c2, err := Load(out)
	if err != nil {
		t.Fatalf("Load saved: %v", err)
	}
	if c2.Defaults.Transport != "ssh" || c2.Remotes["evil8"].Transport != "quic" {
		t.Fatalf("saved transport keys: defaults=%q remote=%q", c2.Defaults.Transport, c2.Remotes["evil8"].Transport)
	}
}

func TestProtocolsKeysRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	body := "version: 1\ndefaults:\n  protocols: tcp,udp\nremotes:\n  evil8:\n    host: gw\n    protocols: tcp\n"
	if err := writeFile(p, body); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Defaults.Protocols != "tcp,udp" || c.Remotes["evil8"].Protocols != "tcp" {
		t.Fatalf("protocols keys: defaults=%q remote=%q", c.Defaults.Protocols, c.Remotes["evil8"].Protocols)
	}
	out := filepath.Join(dir, "out.yaml")
	if err := Save(out, c); err != nil {
		t.Fatalf("Save: %v", err)
	}
	c2, err := Load(out)
	if err != nil {
		t.Fatalf("Load saved: %v", err)
	}
	if c2.Defaults.Protocols != "tcp,udp" || c2.Remotes["evil8"].Protocols != "tcp" {
		t.Fatalf("saved protocols keys: defaults=%q remote=%q", c2.Defaults.Protocols, c2.Remotes["evil8"].Protocols)
	}
}
