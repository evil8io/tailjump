package config

import (
	"path/filepath"
	"testing"
)

func TestLoadMissingFileReturnsZero(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "absent.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c == nil || c.Version != 0 || len(c.Remotes) != 0 {
		t.Fatalf("want zero config, got %+v", c)
	}
}

func TestLoadParsesRemotes(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	body := "version: 1\ndefaults:\n  user: root\n  dns: split\nexclude:\n  - 192.168.0.0/16\nremotes:\n  evil8:\n    host: gw\n    dns: all\n"
	if err := writeFile(p, body); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Defaults.User != "root" || c.Defaults.DNS != "split" {
		t.Fatalf("defaults: %+v", c.Defaults)
	}
	if c.Remotes["evil8"].Host != "gw" || c.Remotes["evil8"].DNS != "all" {
		t.Fatalf("remote: %+v", c.Remotes["evil8"])
	}
	if len(c.Exclude) != 1 || c.Exclude[0] != "192.168.0.0/16" {
		t.Fatalf("exclude: %+v", c.Exclude)
	}
}
