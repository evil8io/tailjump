package session

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"strings"
	"sync"

	"github.com/evil8io/tailjump/internal/dns"
	"github.com/evil8io/tailjump/internal/mux"
	"github.com/evil8io/tailjump/internal/protocols"
	"github.com/evil8io/tailjump/internal/sshc"
)

// laneDNS is the lane of the flows to the session's DNS servers.
const laneDNS = "dns"

// laneDNSPort is the destination port of those flows.
const laneDNSPort = 53

// lane is one SSH connection to the remote, with its own helper process and
// its own yamux session.
type lane struct {
	name string
	ssh  *sshc.Client
	mux  *mux.Client
}

// laneSet is the set of connections of the SSH transport: the primary lane
// that Run already has, plus one lane per remaining protocol of the set and
// one for the DNS servers. One connection carries one protocol, so a query, a
// datagram, or an echo request does not wait behind a bulk flow.
type laneSet struct {
	extra  []*lane
	dialer *laneDialer
	// names are the lanes that opened, in the order tcp, udp, icmp, dns.
	names string

	stopOnce sync.Once
}

// openLaneSet opens the extra lanes of the SSH transport in parallel and
// returns the set. A lane that does not open is a warning, not an error: its
// protocol then takes the primary lane. A malformed DNS server in the plan is
// an error, because the dns lane cannot route without the server list.
func openLaneSet(ctx context.Context, primary *mux.Client, addr netip.Addr, plan *Plan, set protocols.Set, cacheDir, helperPath string) (*laneSet, error) {
	d := &laneDialer{primary: primary}
	planned := lanePlan(plan, set)
	if len(planned) == 0 {
		return &laneSet{dialer: d}, nil
	}

	if dns.Mode(plan.DNS.Mode) != dns.ModeNone {
		servers, err := parseAddrs(plan.DNS.Servers)
		if err != nil {
			return nil, err
		}
		d.servers = servers
	}

	names := []string{planned[0]}
	d.set(planned[0], primary)

	var extra []*lane
	if !plan.SingleLane {
		extra = openLanes(ctx, planned[1:], addr, plan, cacheDir, helperPath)
		for _, l := range extra {
			names = append(names, l.name)
			d.set(l.name, l.mux)
		}
	}
	return &laneSet{extra: extra, dialer: d, names: strings.Join(names, ",")}, nil
}

// lanePlan returns the lanes of a session, in the order tcp, udp, icmp, dns:
// one per protocol of the set, and the dns lane unless the DNS mode is none,
// because the plan then names no servers. The first lane is the primary lane.
func lanePlan(plan *Plan, set protocols.Set) []string {
	var names []string
	if set.TCP {
		names = append(names, protocols.TCP)
	}
	if set.UDP {
		names = append(names, protocols.UDP)
	}
	if set.ICMP {
		names = append(names, protocols.ICMP)
	}
	if dns.Mode(plan.DNS.Mode) != dns.ModeNone {
		names = append(names, laneDNS)
	}
	return names
}

// openLanes opens one lane per name in parallel and returns those that came
// up, in the order of names.
func openLanes(ctx context.Context, names []string, addr netip.Addr, plan *Plan, cacheDir, helperPath string) []*lane {
	opened := make([]*lane, len(names))
	var wg sync.WaitGroup
	for i, name := range names {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l, err := openLane(ctx, name, addr, plan, cacheDir, helperPath)
			if err != nil {
				slog.Warn("lane did not open, its flows take the primary lane", "lane", name, "error", err)
				return
			}
			opened[i] = l
		}()
	}
	wg.Wait()

	lanes := make([]*lane, 0, len(names))
	for _, l := range opened {
		if l != nil {
			lanes = append(lanes, l)
		}
	}
	return lanes
}

// openLane opens the SSH connection of one lane, starts a helper on it from
// the file the primary lane uploaded, and opens the mux over that helper.
func openLane(ctx context.Context, name string, addr netip.Addr, plan *Plan, cacheDir, helperPath string) (*lane, error) {
	client, err := sshc.Dial(ctx, addr, plan.Remote, plan.User, cacheDir)
	if err != nil {
		return nil, fmt.Errorf("ssh dial: %w", err)
	}
	muxClient, err := execHelper(client, helperPath)
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	return &lane{name: name, ssh: client, mux: muxClient}, nil
}

// closed reports the name of the first extra lane whose yamux session ends.
// A closed lane ends the session, by the same path as a closed primary mux.
func (ls *laneSet) closed() <-chan string {
	if ls == nil {
		return nil
	}
	ch := make(chan string, len(ls.extra))
	for _, l := range ls.extra {
		go func() {
			<-l.mux.Wait()
			ch <- l.name
		}()
	}
	return ch
}

// stop sends quit on every extra lane's control stream and closes its mux and
// its SSH connection, so the lane's helper exits.
func (ls *laneSet) stop() {
	if ls == nil {
		return
	}
	ls.stopOnce.Do(func() {
		for _, l := range ls.extra {
			if err := l.mux.Quit(); err != nil {
				slog.Debug("quit lane helper", "lane", l.name, "error", err)
			}
			_ = l.mux.Close()
			_ = l.ssh.Close()
		}
	})
}

// close closes every extra lane without the quit verb. A loss means the peer
// is gone, and a control write would then wait for the yamux write timeout
// on every lane. The lane's helper exits when its SSH connection closes.
// The SSH connection closes before the mux, for the reason in closeTransport.
func (ls *laneSet) close() {
	if ls == nil {
		return
	}
	ls.stopOnce.Do(func() {
		for _, l := range ls.extra {
			_ = l.ssh.Close()
			_ = l.mux.Close()
		}
	})
}

// laneDialer routes a flow to the lane of its protocol, and a flow to a
// session DNS server on port 53 to the dns lane. A protocol whose lane did
// not open, and every flow when there are no extra lanes, takes the primary
// lane.
type laneDialer struct {
	primary mux.Dialer
	tcp     mux.Dialer
	udp     mux.Dialer
	icmp    mux.Dialer
	dns     mux.Dialer
	servers []netip.Addr
}

var _ mux.Dialer = (*laneDialer)(nil)

func (d *laneDialer) set(name string, client mux.Dialer) {
	switch name {
	case protocols.TCP:
		d.tcp = client
	case protocols.UDP:
		d.udp = client
	case protocols.ICMP:
		d.icmp = client
	case laneDNS:
		d.dns = client
	}
}

func (d *laneDialer) DialTCP(dst netip.AddrPort) (net.Conn, error) {
	return d.pick(d.tcp, dst).DialTCP(dst)
}

func (d *laneDialer) DialUDP(dst netip.AddrPort) (*mux.UDPConn, error) {
	return d.pick(d.udp, dst).DialUDP(dst)
}

func (d *laneDialer) DialICMP(dst netip.Addr, ident uint16) (*mux.EchoConn, error) {
	return d.orPrimary(d.icmp).DialICMP(dst, ident)
}

// pick returns the dns lane for a query to a session DNS server, and the
// protocol's lane for every other flow. Port 53 on another address is not a
// session query, so it stays on its protocol's lane.
func (d *laneDialer) pick(protoLane mux.Dialer, dst netip.AddrPort) mux.Dialer {
	if d.dns != nil && dst.Port() == laneDNSPort && d.isServer(dst.Addr()) {
		return d.dns
	}
	return d.orPrimary(protoLane)
}

func (d *laneDialer) orPrimary(protoLane mux.Dialer) mux.Dialer {
	if protoLane != nil {
		return protoLane
	}
	return d.primary
}

func (d *laneDialer) isServer(addr netip.Addr) bool {
	a := addr.Unmap()
	for _, s := range d.servers {
		if s.Unmap() == a {
			return true
		}
	}
	return false
}
