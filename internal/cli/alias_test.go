package cli

import (
	"strings"
	"testing"
)

// TestAliases locks in the short names of the commands, so a rename of a
// command keeps its alias.
func TestAliases(t *testing.T) {
	cases := map[string]string{
		"ls":        "list",
		"up":        "connect",
		"down":      "disconnect",
		"st":        "status",
		"desc":      "describe",
		"remote ls": "list",
		"remote rm": "remove",
	}
	for alias, want := range cases {
		cmd, _, err := newRootCmd().Find(splitArgs(alias))
		if err != nil {
			t.Errorf("alias %q: %v", alias, err)
			continue
		}
		if cmd.Name() != want {
			t.Errorf("alias %q resolved to %q, want %q", alias, cmd.Name(), want)
		}
	}
}

func splitArgs(s string) []string { return strings.Fields(s) }
