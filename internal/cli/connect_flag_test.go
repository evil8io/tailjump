package cli

import "testing"

// TestConnectNetworkAndExcludeFlags locks in that connect can both add a
// route with --network and drop one with --exclude, and that both repeat.
func TestConnectNetworkAndExcludeFlags(t *testing.T) {
	cmd := newConnectCmd()
	for _, name := range []string{"network", "exclude"} {
		f := cmd.Flags().Lookup(name)
		if f == nil {
			t.Fatalf("connect is missing --%s", name)
		}
		if f.Value.Type() != "stringArray" {
			t.Errorf("--%s is %s, want stringArray so it repeats", name, f.Value.Type())
		}
	}
}
