package config

import (
	"path/filepath"
	"reflect"
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

func TestSaveRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "config.yaml")
	want := &Config{
		Version:  1,
		Defaults: Defaults{User: "root", DNS: "split"},
		Exclude:  []string{"192.168.0.0/16"},
		Remotes: map[string]RemoteConfig{
			"evil8": {
				Host:     "gw.example",
				User:     "root",
				DNS:      "all",
				Networks: []string{"10.0.0.0/16"},
				Exclude:  []string{"10.1.0.0/24"},
			},
		},
	}
	if err := Save(p, want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip: got %+v, want %+v", got, want)
	}
}
