//go:build depkeep

// Package depkeep anchors the external dependencies that later chunks import,
// so go.mod is complete now and parallel chunk branches never edit it. This
// file never builds; the depkeep tag is never set. Remove an import here once
// a real package imports the dependency.
package depkeep

import (
	_ "github.com/hashicorp/yamux"
	_ "github.com/vishvananda/netlink"
	_ "golang.org/x/crypto/ssh"
	_ "gvisor.dev/gvisor/pkg/tcpip/stack"
)
