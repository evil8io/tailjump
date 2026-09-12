// Package congestion applies the congestion controller of a session to a
// QUIC connection. BBR is the default, and Brutal sends at a configured
// rate. Both controllers are copied from hysteria; see LICENSE.hysteria.
package congestion

import (
	"fmt"

	quic "github.com/apernet/quic-go"
	quiccongestion "github.com/apernet/quic-go/congestion"

	"github.com/evil8io/tailjump/internal/congestion/bbr"
	"github.com/evil8io/tailjump/internal/congestion/brutal"
	"github.com/evil8io/tailjump/internal/transport"
)

// Apply installs the controller on the connection's sender. Call it right
// after the handshake, on both sides.
func Apply(conn *quic.Conn, ctl transport.Controller) error {
	switch ctl.Name {
	case transport.BBR:
		conn.SetCongestionControl(bbr.NewBbrSender(
			bbr.DefaultClock{},
			initialSize(conn),
			bbr.ProfileStandard,
		))
		return nil
	case transport.Brutal:
		if ctl.Bps == 0 {
			return fmt.Errorf("congestion: brutal needs a rate")
		}
		conn.SetCongestionControl(brutal.NewBrutalSender(ctl.Bps, false))
		return nil
	default:
		return fmt.Errorf("congestion: unknown controller %q", ctl.Name)
	}
}

// initialSize keeps the controller's datagram size at or below the size QUIC
// starts with, as hysteria does, so the two never disagree on the packet
// size.
func initialSize(conn *quic.Conn) quiccongestion.ByteCount {
	byAddr := bbr.GetInitialPacketSize(conn.RemoteAddr())
	if quicSize := conn.InitialPacketSize(); quicSize > 0 {
		return min(quicSize, byAddr)
	}
	return byAddr
}
