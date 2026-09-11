// Command tj gives an engineer a session into a remote network over
// Tailscale SSH.
package main

import (
	"fmt"
	"os"

	"github.com/evil8io/tailjump/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}
