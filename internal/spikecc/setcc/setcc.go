// Package setcc installs a congestion controller on an apernet/quic-go
// connection. It is the wiring hysteria keeps in its own internal tree, copied
// here for the K8S-208 spike because an internal path is not importable.
package setcc

import (
	"fmt"

	quic "github.com/apernet/quic-go"
	"github.com/apernet/quic-go/congestion"

	"github.com/evil8io/tailjump/internal/spikecc/bbr"
	"github.com/evil8io/tailjump/internal/spikecc/brutal"
)

// Apply installs the named controller. The name "cubic" leaves the library
// default in place. "brutal" needs a rate in bit/s.
func Apply(conn *quic.Conn, name string, brutalBps uint64) error {
	switch name {
	case "cubic", "":
		return nil
	case "bbr":
		conn.SetCongestionControl(bbr.NewBbrSender(
			bbr.DefaultClock{},
			seed(conn.InitialPacketSize(), bbr.GetInitialPacketSize(conn.RemoteAddr())),
			bbr.ProfileStandard,
		))
		return nil
	case "brutal":
		if brutalBps == 0 {
			return fmt.Errorf("brutal needs a rate")
		}
		conn.SetCongestionControl(brutal.NewBrutalSender(brutalBps, false))
		return nil
	default:
		return fmt.Errorf("unknown congestion controller %q", name)
	}
}

// seed keeps the controller's datagram size at or below the size QUIC starts
// with, as hysteria does, so a path MTU probe between the two does not look
// like a decrease to the controller.
func seed(quicSize, byAddr congestion.ByteCount) congestion.ByteCount {
	if quicSize <= 0 {
		return byAddr
	}
	return min(quicSize, byAddr)
}
