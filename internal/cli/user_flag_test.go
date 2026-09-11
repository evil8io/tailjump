package cli

import "testing"

// TestSSHUserFlagPresent locks in that every command which opens SSH to a
// remote accepts --user, so a gateway that requires a non-default user, for
// example root, is reachable from each of them.
func TestSSHUserFlagPresent(t *testing.T) {
	cmds := map[string]bool{
		"list":     true,
		"describe": true,
		"doctor":   true,
		"connect":  true,
	}
	for _, cmd := range newRootCmd().Commands() {
		if !cmds[cmd.Name()] {
			continue
		}
		if cmd.Flags().Lookup("user") == nil {
			t.Errorf("command %q is missing the --user flag", cmd.Name())
		}
	}
}
