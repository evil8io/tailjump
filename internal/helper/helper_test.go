package helper

import (
	"strings"
	"testing"
)

func TestArchForUname(t *testing.T) {
	cases := map[string]string{
		"x86_64":  "amd64",
		"aarch64": "arm64",
	}
	for unameM, want := range cases {
		got, err := ArchForUname(unameM)
		if err != nil {
			t.Fatalf("ArchForUname(%q): %v", unameM, err)
		}
		if got != want {
			t.Fatalf("ArchForUname(%q) = %q, want %q", unameM, got, want)
		}
	}
}

func TestArchForUnameUnsupported(t *testing.T) {
	_, err := ArchForUname("armv7l")
	if err == nil {
		t.Fatal("ArchForUname(\"armv7l\") returned no error")
	}
	if !strings.Contains(err.Error(), "unsupported remote architecture") {
		t.Fatalf("error = %q, want it to name the unsupported architecture", err.Error())
	}
}
