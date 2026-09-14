// Command tj gives an engineer a session into a remote network over
// Tailscale SSH.
package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/evil8io/tailjump/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(exitCode(err))
	}
}

// exitCode returns the code of err's ExitError. Every other error from
// Execute is a usage error, exit 2: the unknown command, the Args
// validators, the flag parse error, and the required-flag error. See
// docs/architecture.md, "CLI conventions".
func exitCode(err error) int {
	var ee *cli.ExitError
	if errors.As(err, &ee) {
		return ee.Code
	}
	return 2
}
