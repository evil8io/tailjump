package session

import (
	"bytes"
	"io"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/evil8io/tailjump/internal/dns"
	"github.com/evil8io/tailjump/internal/mux"
	"github.com/evil8io/tailjump/internal/protocols"
)

// fakeDialer records the flows a lane receives. The dial itself returns
// nothing, because the routing tests assert the lane, not the flow.
type fakeDialer struct {
	tcp  []netip.AddrPort
	udp  []netip.AddrPort
	icmp []netip.Addr
}

func (f *fakeDialer) DialTCP(dst netip.AddrPort) (net.Conn, error) {
	f.tcp = append(f.tcp, dst)
	return nil, nil
}

func (f *fakeDialer) DialUDP(dst netip.AddrPort) (*mux.UDPConn, error) {
	f.udp = append(f.udp, dst)
	return nil, nil
}

func (f *fakeDialer) DialICMP(dst netip.Addr, _ uint16) (*mux.EchoConn, error) {
	f.icmp = append(f.icmp, dst)
	return nil, nil
}

func (f *fakeDialer) calls() int { return len(f.tcp) + len(f.udp) + len(f.icmp) }

// TestLanePlan locks in the lanes a session opens: one per protocol of the
// set in the order tcp, udp, icmp, then the dns lane unless the mode is none.
// The first name is the primary lane.
func TestLanePlan(t *testing.T) {
	cases := []struct {
		set  string
		mode dns.Mode
		want []string
	}{
		{"tcp,udp,icmp", dns.ModeSplit, []string{"tcp", "udp", "icmp", "dns"}},
		{"tcp,udp,icmp", dns.ModeAll, []string{"tcp", "udp", "icmp", "dns"}},
		{"tcp,udp,icmp", dns.ModeNone, []string{"tcp", "udp", "icmp"}},
		{"udp,icmp", dns.ModeSplit, []string{"udp", "icmp", "dns"}},
		{"tcp", dns.ModeNone, []string{"tcp"}},
	}
	for _, c := range cases {
		set, err := protocols.Parse(c.set)
		if err != nil {
			t.Fatal(err)
		}
		got := lanePlan(&Plan{DNS: PlanDNS{Mode: string(c.mode)}}, set)
		if len(got) != len(c.want) {
			t.Fatalf("lanePlan(%s, %s) = %v, want %v", c.set, c.mode, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("lanePlan(%s, %s) = %v, want %v", c.set, c.mode, got, c.want)
			}
		}
	}
}

// TestLaneDialerRoutesByProtocol checks that each protocol reaches its own
// lane, that a query to a session DNS server on port 53 takes the dns lane
// over UDP and over TCP, and that port 53 on another address stays on the
// protocol lane.
func TestLaneDialerRoutesByProtocol(t *testing.T) {
	tcpLane := &fakeDialer{}
	udpLane := &fakeDialer{}
	icmpLane := &fakeDialer{}
	dnsLane := &fakeDialer{}
	primary := &fakeDialer{}
	server := netip.MustParseAddr("10.0.0.2")
	d := &laneDialer{
		primary: primary,
		tcp:     tcpLane,
		udp:     udpLane,
		icmp:    icmpLane,
		dns:     dnsLane,
		servers: []netip.Addr{server},
	}

	if _, err := d.DialTCP(netip.MustParseAddrPort("10.0.0.10:443")); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DialUDP(netip.MustParseAddrPort("10.0.0.10:443")); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DialICMP(netip.MustParseAddr("10.0.0.10"), 1); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DialUDP(netip.AddrPortFrom(server, 53)); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DialTCP(netip.AddrPortFrom(server, 53)); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DialUDP(netip.MustParseAddrPort("10.0.0.99:53")); err != nil {
		t.Fatal(err)
	}

	if len(tcpLane.tcp) != 1 || tcpLane.calls() != 1 {
		t.Fatalf("tcp lane = %+v, want the one tcp flow", tcpLane)
	}
	if len(udpLane.udp) != 2 || udpLane.calls() != 2 {
		t.Fatalf("udp lane = %+v, want the udp flow and the query to the non-server address", udpLane)
	}
	if len(icmpLane.icmp) != 1 || icmpLane.calls() != 1 {
		t.Fatalf("icmp lane = %+v, want the one echo flow", icmpLane)
	}
	if len(dnsLane.udp) != 1 || len(dnsLane.tcp) != 1 || dnsLane.calls() != 2 {
		t.Fatalf("dns lane = %+v, want the udp query and the tcp query", dnsLane)
	}
	if primary.calls() != 0 {
		t.Fatalf("primary lane = %+v, want no flow while every lane is up", primary)
	}
}

// TestLaneDialerFallsBackToPrimary checks that a protocol whose lane did not
// open takes the primary lane, and that a query to a DNS server takes its
// protocol lane when there is no dns lane.
func TestLaneDialerFallsBackToPrimary(t *testing.T) {
	primary := &fakeDialer{}
	udpLane := &fakeDialer{}
	server := netip.MustParseAddr("10.0.0.2")
	d := &laneDialer{primary: primary, udp: udpLane, servers: []netip.Addr{server}}

	if _, err := d.DialTCP(netip.MustParseAddrPort("10.0.0.10:443")); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DialICMP(netip.MustParseAddr("10.0.0.10"), 1); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DialUDP(netip.AddrPortFrom(server, 53)); err != nil {
		t.Fatal(err)
	}

	if len(primary.tcp) != 1 || len(primary.icmp) != 1 || primary.calls() != 2 {
		t.Fatalf("primary lane = %+v, want the tcp flow and the echo flow", primary)
	}
	if len(udpLane.udp) != 1 || udpLane.calls() != 1 {
		t.Fatalf("udp lane = %+v, want the query, because there is no dns lane", udpLane)
	}
}

// countingDialer counts the flows that reach one lane and passes them on.
type countingDialer struct {
	inner mux.Dialer
	tcp   int
	udp   int
}

func (c *countingDialer) DialTCP(dst netip.AddrPort) (net.Conn, error) {
	c.tcp++
	return c.inner.DialTCP(dst)
}

func (c *countingDialer) DialUDP(dst netip.AddrPort) (*mux.UDPConn, error) {
	c.udp++
	return c.inner.DialUDP(dst)
}

func (c *countingDialer) DialICMP(dst netip.Addr, ident uint16) (*mux.EchoConn, error) {
	return c.inner.DialICMP(dst, ident)
}

// laneLoopback wires one lane to its own mux server over net.Pipe, in one
// process, as a separate SSH connection and helper would.
func laneLoopback(t *testing.T) *countingDialer {
	t.Helper()
	c1, c2 := net.Pipe()
	srv := &mux.Server{}
	go func() { _ = srv.Serve(c2) }()
	client, err := mux.NewClient(c1)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() {
		_ = client.Close()
		_ = c2.Close()
	})
	return &countingDialer{inner: client}
}

func tcpEchoListener(t *testing.T) netip.AddrPort {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	return ln.Addr().(*net.TCPAddr).AddrPort()
}

func udpEchoServer(t *testing.T) netip.AddrPort {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	go func() {
		buf := make([]byte, 65535)
		for {
			n, addr, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			if _, err := conn.WriteToUDP(buf[:n], addr); err != nil {
				return
			}
		}
	}()
	return conn.LocalAddr().(*net.UDPAddr).AddrPort()
}

// TestLaneDialerLoopback runs a TCP echo and a UDP echo through a lane dialer
// with two helpers in one process, and checks that each flow used the lane of
// its protocol.
func TestLaneDialerLoopback(t *testing.T) {
	tcpLane := laneLoopback(t)
	udpLane := laneLoopback(t)
	d := &laneDialer{primary: tcpLane, tcp: tcpLane, udp: udpLane}

	conn, err := d.DialTCP(tcpEchoListener(t))
	if err != nil {
		t.Fatalf("DialTCP: %v", err)
	}
	defer func() { _ = conn.Close() }()
	want := []byte("hello over the tcp lane")
	if _, err := conn.Write(want); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := make([]byte, len(want))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("tcp echo = %q, want %q", got, want)
	}

	flow, err := d.DialUDP(udpEchoServer(t))
	if err != nil {
		t.Fatalf("DialUDP: %v", err)
	}
	defer func() { _ = flow.Close() }()
	datagram := []byte("hello over the udp lane")
	if err := flow.WriteDatagram(64, datagram); err != nil {
		t.Fatalf("WriteDatagram: %v", err)
	}
	_ = flow.SetReadDeadline(time.Now().Add(5 * time.Second))
	reply, err := flow.ReadReply()
	if err != nil {
		t.Fatalf("ReadReply: %v", err)
	}
	if !bytes.Equal(reply.Payload, datagram) {
		t.Fatalf("udp echo = %q, want %q", reply.Payload, datagram)
	}

	if tcpLane.tcp != 1 || tcpLane.udp != 0 {
		t.Fatalf("tcp lane = %d tcp and %d udp flows, want 1 and 0", tcpLane.tcp, tcpLane.udp)
	}
	if udpLane.udp != 1 || udpLane.tcp != 0 {
		t.Fatalf("udp lane = %d udp and %d tcp flows, want 1 and 0", udpLane.udp, udpLane.tcp)
	}
}
