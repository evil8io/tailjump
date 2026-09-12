// Package dataplane captures every TCP connection, UDP flow, and ICMP echo
// flow to a session network on a TUN device, terminates it in a gVisor
// userspace netstack, and bridges it to the remote helper over the mux. It
// reads and writes the TUN in batches and terminates no other traffic, so
// the netstack sees only the lossless local leg. See docs/architecture.md,
// "Netstack".
package dataplane

import (
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"gvisor.dev/gvisor/pkg/buffer"
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
	"github.com/evil8io/tailjump/internal/protocols"
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

// netStack is the gVisor stack, its link endpoint, the mux the forwarders
// bridge flows onto, and the protocol set that says which forwarders exist.
// A TUN pump feeds the endpoint; the loopback test feeds it from a second
// stack.
type netStack struct {
	stk    *stack.Stack
	ep     *channel.Endpoint
	dialer mux.Dialer
	set    protocols.Set

	echoMu          sync.Mutex
	echoFlows       map[echoKey]*echoFlow
	echoUnsupported atomic.Bool
	echoWarn        sync.Once
}

// newNetStack builds the stack with the network and transport protocols, the
// promiscuous spoofing NIC, the default routes, and the forwarders of the
// protocol set. It creates no device.
func newNetStack(mtu uint32, dialer mux.Dialer, set protocols.Set) (*netStack, error) {
	return newNetStackWith(mtu, dialer, set, []stack.NetworkProtocolFactory{
		ipv4.NewProtocol, ipv6.NewProtocol,
	})
}

// newNetStackWith builds the netstack with the given network protocol
// factories. The loopback test passes factories that accept martian loopback
// destinations, which the production factories reject.
func newNetStackWith(mtu uint32, dialer mux.Dialer, set protocols.Set, netProtos []stack.NetworkProtocolFactory) (*netStack, error) {
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

	ns := &netStack{stk: stk, ep: ep, dialer: dialer, set: set, echoFlows: map[echoKey]*echoFlow{}}

	// Without a forwarder the stack answers itself: a reset for TCP, and a
	// port unreachable for UDP. That is the enforcement of a disabled
	// protocol.
	if set.TCP {
		tcpFwd := tcp.NewForwarder(stk, 0, tcpForwarderMaxInFlight, ns.handleTCP)
		stk.SetTransportProtocolHandler(tcp.ProtocolNumber, tcpFwd.HandlePacket)
	}
	if set.UDP {
		udpFwd := udp.NewForwarder(stk, ns.handleUDP)
		stk.SetTransportProtocolHandler(udp.ProtocolNumber, udpFwd.HandlePacket)
	}

	return ns, nil
}

func (ns *netStack) close() {
	ns.closeEchoFlows()
	ns.ep.Close()
	ns.stk.Close()
}

// writePacket queues a packet the client built itself, an echo reply or an
// ICMP error, on the link endpoint, so the TUN write pump carries it with
// the packets of the stack.
func (ns *netStack) writePacket(pkt []byte) {
	pb := stack.NewPacketBuffer(stack.PacketBufferOptions{
		Payload: buffer.MakeWithData(pkt),
	})
	var list stack.PacketBufferList
	list.PushBack(pb)
	_, _ = ns.ep.WritePackets(list)
	pb.DecRef()
}

// handleTCP bridges one captured TCP connection to a mux TCP stream. It runs
// the mux dial and the endpoint handshake in a goroutine, because
// CreateEndpoint blocks on the three-way handshake and must not stall the
// packet dispatch path.
func (ns *netStack) handleTCP(r *tcp.ForwarderRequest) {
	id := r.ID()
	dst := netip.AddrPortFrom(addrFrom(id.LocalAddress), id.LocalPort)
	go func() {
		stream, err := ns.dialer.DialTCP(dst)
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
// runs inline, then the relay runs in a goroutine. The endpoint reports the
// TTL of each datagram, so a traceroute probe keeps its TTL on the remote.
func (ns *netStack) handleUDP(r *udp.ForwarderRequest) {
	id := r.ID()
	src := netip.AddrPortFrom(addrFrom(id.RemoteAddress), id.RemotePort)
	dst := netip.AddrPortFrom(addrFrom(id.LocalAddress), id.LocalPort)
	var wq waiter.Queue
	ep, terr := r.CreateEndpoint(&wq)
	if terr != nil {
		slog.Debug("udp create endpoint failed", "dst", dst, "error", terr.String())
		return
	}
	ep.SocketOptions().SetReceiveTTL(true)
	ep.SocketOptions().SetReceiveHopLimit(true)
	conn := gonet.NewUDPConn(&wq, ep)
	go ns.relayUDP(conn, ep, &wq, src, dst)
}

func (ns *netStack) relayUDP(conn *gonet.UDPConn, ep tcpip.Endpoint, wq *waiter.Queue, src, dst netip.AddrPort) {
	defer func() { _ = conn.Close() }()
	flow, err := ns.dialer.DialUDP(dst)
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

	// lastLen is the length of the last datagram sent, for the UDP length
	// field of a rebuilt ICMP error.
	var lastLen atomic.Uint32

	go func() {
		defer finish()
		reader := newDatagramReader(ep, wq)
		defer reader.close()
		for {
			payload, ttl, ok := reader.read(idle, done)
			if !ok {
				return
			}
			lastLen.Store(uint32(len(payload)))
			if err := flow.WriteDatagram(ttl, payload); err != nil {
				return
			}
		}
	}()
	go func() {
		defer finish()
		for {
			reply, err := flow.ReadReply()
			if err != nil {
				return
			}
			if reply.Error != nil {
				ns.writeUDPError(*reply.Error, src, dst, int(lastLen.Load()))
				continue
			}
			_ = conn.SetWriteDeadline(time.Now().Add(idle))
			if _, err := conn.Write(reply.Payload); err != nil {
				return
			}
		}
	}()
	<-done
}

// writeUDPError rebuilds the ICMP error for a datagram of the flow and
// writes it to the device, so the application's socket sees the Time
// Exceeded or the Unreachable that the remote received.
func (ns *netStack) writeUDPError(e mux.ICMPError, src, dst netip.AddrPort, lastLen int) {
	if e.From.Is4() != src.Addr().Is4() {
		return
	}
	inner := innerUDP(src, dst, lastLen, e.Inner)
	ns.writePacket(buildICMPError(e, src.Addr(), inner))
}

// datagramReader reads datagrams with their TTL from a UDP endpoint. gonet
// hides the control messages, so the reader uses the endpoint and the wait
// queue itself, the way gonet does.
type datagramReader struct {
	ep    tcpip.Endpoint
	wq    *waiter.Queue
	entry waiter.Entry
	ch    chan struct{}
	buf   []byte
}

func newDatagramReader(ep tcpip.Endpoint, wq *waiter.Queue) *datagramReader {
	r := &datagramReader{ep: ep, wq: wq, buf: make([]byte, header.UDPMaximumPacketSize)}
	r.entry, r.ch = waiter.NewChannelEntry(waiter.ReadableEvents)
	wq.EventRegister(&r.entry)
	return r
}

func (r *datagramReader) close() {
	r.wq.EventUnregister(&r.entry)
}

// read returns the next datagram and its TTL, or false on the idle timeout,
// on done, or when the endpoint closes. The returned slice is valid until
// the next read.
func (r *datagramReader) read(idle time.Duration, done <-chan struct{}) ([]byte, uint8, bool) {
	timer := time.NewTimer(idle)
	defer timer.Stop()
	for {
		w := tcpip.SliceWriter(r.buf)
		res, err := r.ep.Read(&w, tcpip.ReadOptions{})
		if _, ok := err.(*tcpip.ErrWouldBlock); ok {
			select {
			case <-r.ch:
				continue
			case <-timer.C:
				return nil, 0, false
			case <-done:
				return nil, 0, false
			}
		}
		if err != nil {
			return nil, 0, false
		}
		ttl := uint8(defaultTTL)
		switch {
		case res.ControlMessages.HasTTL:
			ttl = res.ControlMessages.TTL
		case res.ControlMessages.HasHopLimit:
			ttl = res.ControlMessages.HopLimit
		}
		return r.buf[:res.Count], ttl, true
	}
}

// addrFrom converts a gVisor address to a netip.Addr, unmapping a 4-in-6 form
// so an IPv4 destination compares and prints as IPv4.
func addrFrom(a tcpip.Address) netip.Addr {
	addr, _ := netip.AddrFromSlice(a.AsSlice())
	return addr.Unmap()
}
