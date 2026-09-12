package session

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"

	"github.com/evil8io/tailjump/internal/mux"
	"github.com/evil8io/tailjump/internal/sshc"
	"github.com/evil8io/tailjump/internal/transport"
)

// selectTransport applies the plan's transport mode: it negotiates and dials
// the QUIC transport, or falls back to the SSH transport, and records the
// outcome in the state. It returns nil when the session runs on the SSH
// transport.
func selectTransport(ctx context.Context, muxClient *mux.Client, addr netip.Addr, plan *Plan, state *State) (*mux.QUICClient, error) {
	mode := transport.Mode(plan.Transport)
	if mode == "" {
		mode = transport.ModeAuto
	}
	if mode == transport.ModeSSH {
		state.Transport = TransportSSH
		return nil, nil
	}
	ports, err := plan.quicPorts()
	if err != nil {
		return nil, err
	}
	q, reason, err := dialQUIC(ctx, muxClient, addr, ports, plan.BandwidthUp, plan.BandwidthDown)
	if err != nil {
		return nil, err
	}
	if q != nil {
		state.Transport = TransportQUIC
		state.QUICPort = q.Port()
		slog.Info("quic transport up", "port", q.Port())
		return q, nil
	}
	if mode == transport.ModeQUIC {
		return nil, fmt.Errorf("quic transport unavailable: %s", reason)
	}
	state.Transport = TransportSSH
	state.Fallback = reason
	slog.Warn("quic transport unavailable, the session uses the ssh transport",
		"reason", reason,
		"policy", policyHint(ports, plan.Remote))
	return nil, nil
}

// policyHint names the tailnet policy rule the QUIC transport needs, for the
// fallback warning.
func policyHint(ports transport.PortRange, remote string) string {
	return fmt.Sprintf("the tailnet policy needs a grant with \"ip\": [%q] from this client to %s", ports.PolicyRule(), remote)
}

// dialQUIC negotiates the listener on the control stream and dials it. A
// helper that cannot listen, or a handshake that does not complete within
// the timeout, returns a nil client with the reason; that is the fallback
// case, not an error.
func dialQUIC(ctx context.Context, muxClient *mux.Client, addr netip.Addr, ports transport.PortRange, up, down uint64) (*mux.QUICClient, string, error) {
	cert, fp, err := mux.NewClientCertificate()
	if err != nil {
		return nil, "", fmt.Errorf("quic client certificate: %w", err)
	}
	reply, err := muxClient.NegotiateQUIC(mux.QUICRequest{
		BindAddr:    addr,
		Ports:       ports,
		Controller:  transport.ControllerFor(down),
		Fingerprint: fp,
	})
	var unavailable *mux.UnavailableError
	if errors.As(err, &unavailable) {
		return nil, unavailable.Reason, nil
	}
	if err != nil {
		return nil, "", err
	}
	q, err := mux.DialQUIC(ctx, netip.AddrPortFrom(addr, reply.Port), cert, reply.Fingerprint, transport.ControllerFor(up))
	if err != nil {
		if aerr := muxClient.AbandonQUIC(); aerr != nil {
			slog.Debug("abandon quic", "error", aerr)
		}
		return nil, fmt.Sprintf("the handshake to udp port %d did not complete: %v", reply.Port, err), nil
	}
	return q, "", nil
}

func (p *Plan) quicPorts() (transport.PortRange, error) {
	if p.QUICPorts == "" {
		return transport.DefaultPorts, nil
	}
	r, err := transport.ParsePortRange(p.QUICPorts)
	if err != nil {
		return transport.PortRange{}, fmt.Errorf("plan quic_ports: %w", err)
	}
	return r, nil
}

// ProbeQUIC uploads the helper, negotiates the QUIC transport, dials it, and
// tears everything down. tj doctor uses it to report whether the transport
// comes up on the remote. It returns the helper's port.
func ProbeQUIC(ctx context.Context, client *sshc.Client, arch string, addr netip.Addr, ports transport.PortRange, up, down uint64) (uint16, error) {
	muxClient, err := startHelper(client, arch)
	if err != nil {
		return 0, err
	}
	defer func() { _ = muxClient.Close() }()
	defer func() { _ = muxClient.Quit() }()
	q, reason, err := dialQUIC(ctx, muxClient, addr, ports, up, down)
	if err != nil {
		return 0, err
	}
	if q == nil {
		return 0, errors.New(reason)
	}
	port := q.Port()
	_ = q.Close()
	return port, nil
}
