// Package embed embeds the built tj-helper binaries so cmd/tj can upload
// one to a remote without a separate distribution step.
package embed

import (
	"embed"
	"fmt"
)

//go:embed all:bin
var bin embed.FS

// Helper returns the built tj-helper binary for linux/goarch. Run
// `task helpers` before `connect` needs a binary that is not yet built.
func Helper(goarch string) ([]byte, error) {
	data, err := bin.ReadFile(fmt.Sprintf("bin/tj-helper-linux-%s", goarch))
	if err != nil {
		return nil, fmt.Errorf("helper for linux/%s is not built; run task helpers", goarch)
	}
	return data, nil
}
