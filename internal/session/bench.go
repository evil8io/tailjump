package session

import (
	"context"
	"fmt"
	"net/netip"
	"time"

	"github.com/evil8io/tailjump/internal/mux"
	"github.com/evil8io/tailjump/internal/sshc"
	"github.com/evil8io/tailjump/internal/transport"
)

// BenchReport is what tj bench measured: the transport it ran on, and one
// result per direction.
type BenchReport struct {
	Transport string
	QUICPort  uint16
	Fallback  string
	Up        mux.BenchResult
	Down      mux.BenchResult
}

// benchDialer carries the bench streams. The yamux Client and the QUICClient
// both have Bench.
type benchDialer interface {
	Bench(ctx context.Context, dir mux.BenchDirection, d time.Duration) (mux.BenchResult, error)
}

// Bench starts a temporary helper, brings the transport up, and measures the
// throughput up and then down. The helper removes its own file at its exit,
// so no file stays on the remote.
//
// The congestion controller is BBR in both directions. A manifest bandwidth
// would select Brutal, and the run would then measure the configured rate
// instead of the path.
func Bench(ctx context.Context, client *sshc.Client, arch string, addr netip.Addr, mode transport.Mode, ports transport.PortRange, d time.Duration) (BenchReport, error) {
	muxClient, _, err := startHelper(client, arch)
	if err != nil {
		return BenchReport{}, err
	}
	defer func() { _ = muxClient.Close() }()
	defer func() { _ = muxClient.Quit() }()

	report := BenchReport{Transport: TransportSSH}
	var dialer benchDialer = muxClient
	if mode != transport.ModeSSH {
		q, reason, derr := dialQUIC(ctx, muxClient, addr, ports, transport.ControllerFor(0), transport.ControllerFor(0))
		switch {
		case derr != nil:
			return BenchReport{}, derr
		case q != nil:
			defer func() { _ = q.Close() }()
			report.Transport, report.QUICPort, dialer = TransportQUIC, q.Port(), q
		case mode == transport.ModeQUIC:
			return BenchReport{}, fmt.Errorf("quic transport unavailable: %s", reason)
		default:
			report.Fallback = reason
		}
	}

	if report.Up, err = dialer.Bench(ctx, mux.BenchUp, d); err != nil {
		return BenchReport{}, fmt.Errorf("bench up: %w", err)
	}
	if report.Down, err = dialer.Bench(ctx, mux.BenchDown, d); err != nil {
		return BenchReport{}, fmt.Errorf("bench down: %w", err)
	}
	return report, nil
}
