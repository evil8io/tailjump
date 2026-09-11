package dataplane

import (
	"bytes"
	"context"
	"net"
	"net/netip"
	"testing"
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

	"github.com/evil8io/tailjump/internal/mux"
)

const testMTU = 1500

var (
	appV4 = netip.MustParseAddr("169.254.10.2")
	appV6 = netip.MustParseAddr("fd00:117::2")
)

// loopbackNetProtos accept martian loopback traffic, because the test routes
// to a real listener at 127.0.0.1 or ::1.
func loopbackNetProtos() []stack.NetworkProtocolFactory {
	return []stack.NetworkProtocolFactory{
		ipv4.NewProtocolWithOptions(ipv4.Options{AllowExternalLoopbackTraffic: true}),
		ipv6.NewProtocolWithOptions(ipv6.Options{AllowExternalLoopbackTraffic: true}),
	}
}

// loopback wires the netstack under test to a second "application" stack over
// a pair of channel endpoints, and runs the mux client against an in-process
// helper over net.Pipe. It needs no device and no root.
type loopback struct {
	app    *stack.Stack
	ns     *netStack
	client *mux.Client
	cancel context.CancelFunc
}

func newLoopback(t *testing.T) *loopback {
	t.Helper()

	clientPipe, helperPipe := net.Pipe()
	go func() {
		srv := &mux.Server{Info: mux.ControlInfo{
			Version: "test", GOOS: "linux", GOARCH: "amd64", Hostname: "helper", PID: 1,
		}}
		_ = srv.Serve(helperPipe)
	}()
	client, err := mux.NewClient(clientPipe)
	if err != nil {
		t.Fatalf("mux client: %v", err)
	}

	ns, err := newNetStackWith(testMTU, client, loopbackNetProtos())
	if err != nil {
		t.Fatalf("netstack: %v", err)
	}

	app, appEp := newAppStack(t)

	ctx, cancel := context.WithCancel(context.Background())
	go linkPump(ctx, appEp, ns.ep)
	go linkPump(ctx, ns.ep, appEp)

	lb := &loopback{app: app, ns: ns, client: client, cancel: cancel}
	t.Cleanup(func() {
		cancel()
		_ = client.Close()
		ns.close()
		app.Close()
	})
	return lb
}

// newAppStack builds the stack that stands in for the client's applications:
// two assigned addresses and default routes, so a Dial reaches any
// destination through its link endpoint.
func newAppStack(t *testing.T) (*stack.Stack, *channel.Endpoint) {
	t.Helper()
	stk := stack.New(stack.Options{
		NetworkProtocols: loopbackNetProtos(),
		TransportProtocols: []stack.TransportProtocolFactory{
			tcp.NewProtocol, udp.NewProtocol, icmp.NewProtocol4, icmp.NewProtocol6,
		},
	})
	ep := channel.New(channelQueueSize, testMTU, "")
	if err := stk.CreateNIC(nicID, ep); err != nil {
		t.Fatalf("app create nic: %v", err)
	}
	for _, a := range []netip.Addr{appV4, appV6} {
		pa := tcpip.ProtocolAddress{
			Protocol:          protoNumber(a),
			AddressWithPrefix: tcpip.AddrFromSlice(a.AsSlice()).WithPrefix(),
		}
		if err := stk.AddProtocolAddress(nicID, pa, stack.AddressProperties{}); err != nil {
			t.Fatalf("app add address %s: %v", a, err)
		}
	}
	if err := stk.SetSpoofing(nicID, true); err != nil {
		t.Fatalf("app spoofing: %v", err)
	}
	stk.SetRouteTable([]tcpip.Route{
		{Destination: header.IPv4EmptySubnet, NIC: nicID},
		{Destination: header.IPv6EmptySubnet, NIC: nicID},
	})
	return stk, ep
}

// linkPump copies every packet one endpoint emits into the other as inbound,
// which joins the two stacks over a virtual link.
func linkPump(ctx context.Context, from, to *channel.Endpoint) {
	for {
		pb := from.ReadContext(ctx)
		if pb == nil {
			return
		}
		view := pb.ToView()
		npb := stack.NewPacketBuffer(stack.PacketBufferOptions{
			Payload: buffer.MakeWithData(view.AsSlice()),
		})
		to.InjectInbound(pb.NetworkProtocolNumber, npb)
		npb.DecRef()
		view.Release()
		pb.DecRef()
	}
}

func protoNumber(a netip.Addr) tcpip.NetworkProtocolNumber {
	if a.Is4() {
		return ipv4.ProtocolNumber
	}
	return ipv6.ProtocolNumber
}

func fullAddr(ap netip.AddrPort) tcpip.FullAddress {
	return tcpip.FullAddress{
		Addr: tcpip.AddrFromSlice(ap.Addr().AsSlice()),
		Port: ap.Port(),
	}
}

func TestLoopbackTCPv4(t *testing.T) {
	testLoopbackTCP(t, "127.0.0.1:0")
}

func TestLoopbackTCPv6(t *testing.T) {
	if !hasIPv6Loopback() {
		t.Skip("no IPv6 loopback on this host")
	}
	testLoopbackTCP(t, "[::1]:0")
}

func testLoopbackTCP(t *testing.T, listenAddr string) {
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		buf := make([]byte, 64)
		n, err := conn.Read(buf)
		if err != nil {
			return
		}
		_, _ = conn.Write(buf[:n])
	}()

	lb := newLoopback(t)
	dst := netip.MustParseAddrPort(ln.Addr().String())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := gonet.DialContextTCP(ctx, lb.app, fullAddr(dst), protoNumber(dst.Addr()))
	if err != nil {
		t.Fatalf("dial through netstack: %v", err)
	}
	defer func() { _ = conn.Close() }()

	want := []byte("hello over the netstack")
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	if _, err := conn.Write(want); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := make([]byte, len(want))
	if _, err := readFull(conn, got); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("echo = %q, want %q", got, want)
	}
}

func TestLoopbackUDPv4(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen udp: %v", err)
	}
	defer func() { _ = pc.Close() }()
	go func() {
		buf := make([]byte, 1500)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			_, _ = pc.WriteTo(buf[:n], addr)
		}
	}()

	lb := newLoopback(t)
	dst := netip.MustParseAddrPort(pc.LocalAddr().String())

	raddr := fullAddr(dst)
	conn, err := gonet.DialUDP(lb.app, nil, &raddr, protoNumber(dst.Addr()))
	if err != nil {
		t.Fatalf("dial udp through netstack: %v", err)
	}
	defer func() { _ = conn.Close() }()

	want := []byte("udp over the netstack")
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	if _, err := conn.Write(want); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := make([]byte, len(want))
	n, err := conn.Read(got)
	if err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if !bytes.Equal(got[:n], want) {
		t.Fatalf("echo = %q, want %q", got[:n], want)
	}
}

func readFull(r net.Conn, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := r.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

func hasIPv6Loopback() bool {
	ln, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}
