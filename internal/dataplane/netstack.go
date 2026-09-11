// Package dataplane captures every TCP connection and UDP flow to a session
// network on a TUN device, terminates it in a gVisor userspace netstack, and
// bridges it to the remote helper over the mux. It reads and writes the TUN in
// batches and terminates no other traffic, so the netstack sees only the
// lossless local leg. See docs/architecture.md, "Netstack".
package dataplane

import (
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"sync"
	"time"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv6"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/icmp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
	"gvisor.dev/gvisor/pkg/waiter"

	"github.com/evil8io/tailjump/internal/mux"
)

const (
	nicID tcpip.NICID = 1

	// channelQueueSize is the depth of the link endpoint's outbound queue.
	channelQueueSize = 1024
	// tcpForwarderMaxInFlight caps concurrent TCP handshakes in flight.
	tcpForwarderMaxInFlight = 2048

	dnsPort    = 53
	udpIdle    = 60 * time.Second
	udpIdleDNS = 10 * time.Second

	copyBufSize = 256 << 10
)

// netStack is the gVisor stack, its link endpoint, and the mux the forwarders
// bridge flows onto. A TUN pump feeds the endpoint; the loopback test feeds it
// from a second stack.
type netStack struct {
	stk    *stack.Stack
	ep     *channel.Endpoint
	client *mux.Client
}

// newNetStack builds the stack with the network and transport protocols, the
// promiscuous spoofing NIC, the default routes, and the TCP and UDP
// forwarders. It creates no device.
func newNetStack(mtu uint32, client *mux.Client) (*netStack, error) {
	return newNetStackWith(mtu, client, []stack.NetworkProtocolFactory{
		ipv4.NewProtocol, ipv6.NewProtocol,
	})
}

// newNetStackWith builds the netstack with the given network protocol
// factories. The loopback test passes factories that accept martian loopback
// destinations, which the production factories reject.
func newNetStackWith(mtu uint32, client *mux.Client, netProtos []stack.NetworkProtocolFactory) (*netStack, error) {
	stk := stack.New(stack.Options{
		NetworkProtocols: netProtos,
		TransportProtocols: []stack.TransportProtocolFactory{
			tcp.NewProtocol, udp.NewProtocol, icmp.NewProtocol4, icmp.NewProtocol6,
		},
	})

	ep := channel.New(channelQueueSize, mtu, "")
	if err := stk.CreateNIC(nicID, ep); err != nil {
		return nil, fmt.Errorf("create nic: %v", err)
	}
	// Promiscuous mode accepts every destination, and spoofing lets the
	// endpoints answer from the captured destination address.
	if err := stk.SetPromiscuousMode(nicID, true); err != nil {
		return nil, fmt.Errorf("set promiscuous mode: %v", err)
	}
	if err := stk.SetSpoofing(nicID, true); err != nil {
		return nil, fmt.Errorf("set spoofing: %v", err)
	}
	stk.SetRouteTable([]tcpip.Route{
		{Destination: header.IPv4EmptySubnet, NIC: nicID},
		{Destination: header.IPv6EmptySubnet, NIC: nicID},
	})

	ns := &netStack{stk: stk, ep: ep, client: client}

	tcpFwd := tcp.NewForwarder(stk, 0, tcpForwarderMaxInFlight, ns.handleTCP)
	stk.SetTransportProtocolHandler(tcp.ProtocolNumber, tcpFwd.HandlePacket)
	udpFwd := udp.NewForwarder(stk, ns.handleUDP)
	stk.SetTransportProtocolHandler(udp.ProtocolNumber, udpFwd.HandlePacket)

	return ns, nil
}

func (ns *netStack) close() {
	ns.ep.Close()
	ns.stk.Close()
}

// handleTCP bridges one captured TCP connection to a mux TCP stream. It runs
// the mux dial and the endpoint handshake in a goroutine, because
// CreateEndpoint blocks on the three-way handshake and must not stall the
// packet dispatch path.
func (ns *netStack) handleTCP(r *tcp.ForwarderRequest) {
	id := r.ID()
	dst := netip.AddrPortFrom(addrFrom(id.LocalAddress), id.LocalPort)
	go func() {
		stream, err := ns.client.DialTCP(dst)
		if err != nil {
			slog.Debug("tcp dial failed", "dst", dst, "error", err)
			r.Complete(true)
			return
		}
		var wq waiter.Queue
		ep, terr := r.CreateEndpoint(&wq)
		if terr != nil {
			slog.Debug("tcp create endpoint failed", "dst", dst, "error", terr.String())
			_ = stream.Close()
			r.Complete(true)
			return
		}
		r.Complete(false)
		conn := gonet.NewTCPConn(&wq, ep)
		relayTCP(conn, stream)
	}()
}

// relayTCP copies both ways with a half-close: the first EOF closes only the
// write side of the peer, so the other direction can drain.
func relayTCP(conn *gonet.TCPConn, stream io.ReadWriteCloser) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		buf := make([]byte, copyBufSize)
		_, _ = io.CopyBuffer(stream, conn, buf)
		_ = stream.Close()
	}()
	go func() {
		defer wg.Done()
		buf := make([]byte, copyBufSize)
		_, _ = io.CopyBuffer(conn, stream, buf)
		_ = conn.CloseWrite()
	}()
	wg.Wait()
	_ = conn.Close()
	_ = stream.Close()
}

// handleUDP bridges one captured UDP flow to a mux UDP stream. CreateEndpoint
// runs inline, then the relay runs in a goroutine.
func (ns *netStack) handleUDP(r *udp.ForwarderRequest) {
	id := r.ID()
	dst := netip.AddrPortFrom(addrFrom(id.LocalAddress), id.LocalPort)
	var wq waiter.Queue
	ep, terr := r.CreateEndpoint(&wq)
	if terr != nil {
		slog.Debug("udp create endpoint failed", "dst", dst, "error", terr.String())
		return
	}
	conn := gonet.NewUDPConn(&wq, ep)
	go ns.relayUDP(conn, dst)
}

func (ns *netStack) relayUDP(conn *gonet.UDPConn, dst netip.AddrPort) {
	defer func() { _ = conn.Close() }()
	flow, err := ns.client.DialUDP(dst)
	if err != nil {
		slog.Debug("udp dial failed", "dst", dst, "error", err)
		return
	}
	defer func() { _ = flow.Close() }()

	idle := udpIdle
	if dst.Port() == dnsPort {
		idle = udpIdleDNS
	}

	var once sync.Once
	done := make(chan struct{})
	finish := func() { once.Do(func() { close(done) }) }

	go func() {
		defer finish()
		buf := make([]byte, header.UDPMaximumPacketSize)
		for {
			_ = conn.SetReadDeadline(time.Now().Add(idle))
			n, err := conn.Read(buf)
			if err != nil {
				return
			}
			if err := flow.WriteFrame(buf[:n]); err != nil {
				return
			}
		}
	}()
	go func() {
		defer finish()
		for {
			p, err := flow.ReadFrame()
			if err != nil {
				return
			}
			_ = conn.SetWriteDeadline(time.Now().Add(idle))
			if _, err := conn.Write(p); err != nil {
				return
			}
		}
	}()
	<-done
}

// addr converts a gVisor address to a netip.Addr, unmapping a 4-in-6 form so
// an IPv4 destination compares and prints as IPv4.
func addrFrom(a tcpip.Address) netip.Addr {
	addr, _ := netip.AddrFromSlice(a.AsSlice())
	return addr.Unmap()
}
