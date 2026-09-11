package cli

import (
	"testing"

	"github.com/evil8io/tailjump/internal/config"
)

func TestResolveAliasExpandsConfigEntry(t *testing.T) {
	cfg := &config.Config{Remotes: map[string]config.RemoteConfig{
		"evil8": {Host: "gw.example", User: "root"},
	}}

	host, user := resolveAlias(cfg, "evil8")
	if host != "gw.example" || user != "root" {
		t.Fatalf("got host=%q user=%q", host, user)
	}
}

func TestResolveAliasPassesThroughUnknownRef(t *testing.T) {
	cfg := &config.Config{}
	host, user := resolveAlias(cfg, "tag:example")
	if host != "tag:example" || user != "" {
		t.Fatalf("got host=%q user=%q", host, user)
	}
}

func TestSSHUserPrecedence(t *testing.T) {
	cfg := &config.Config{Defaults: config.Defaults{User: "def-user"}}

	if got := sshUser("flag-user", "remote-user", cfg); got != "flag-user" {
		t.Fatalf("flag must win, got %q", got)
	}
	if got := sshUser("", "remote-user", cfg); got != "remote-user" {
		t.Fatalf("remote config must win over defaults, got %q", got)
	}
	if got := sshUser("", "", cfg); got != "def-user" {
		t.Fatalf("defaults must win when nothing else is set, got %q", got)
	}
}

func TestSSHUserFallsBackToLocalUser(t *testing.T) {
	cfg := &config.Config{}
	if got := sshUser("", "", cfg); got == "" {
		t.Fatal("want a non-empty fallback user")
	}
}
