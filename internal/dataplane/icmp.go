package dataplane

import (
	"errors"
	"log/slog"
	"net/netip"
	"sync"
	"time"

	"gvisor.dev/gvisor/pkg/tcpip/header"

	"github.com/evil8io/tailjump/internal/mux"
)

// echoQueueLen is the number of echo requests a flow holds while its stream
// opens. A ping sends one per second, so the queue is generous.
const echoQueueLen = 64

// echoKey identifies one echo flow: the pinged address and the identifier.
// One ping process is one flow, so its sequence numbers never mix with
// another's on the remote.
type echoKey struct {
	dst   netip.Addr
	ident uint16
}

// echoRequest is one captured echo request: the fields the reply needs and
// the frame carries.
type echoRequest struct {
	src     netip.Addr
	seq     uint16
	ttl     uint8
	payload []byte
}

// echoFlow is one echo flow. The pump queues requests; the flow's goroutine
// opens the mux stream, sends the requests, reads the replies and the
// errors, and writes the rebuilt packets to the link endpoint.
type echoFlow struct {
	key   echoKey
	queue chan echoRequest
	conn  *mux.EchoConn

	mu  sync.Mutex
	src netip.Addr
}

func (f *echoFlow) setSrc(a netip.Addr) {
	f.mu.Lock()
	f.src = a
	f.mu.Unlock()
}

func (f *echoFlow) source() netip.Addr {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.src
}

// captureEcho takes an ICMP echo request out of the netstack path and
// reports whether it consumed the packet. It drops the request when the
// protocol set has no icmp, and when the remote has no echo socket. Every
// other packet goes to the netstack.
func (ns *netStack) captureEcho(pkt []byte) bool {
	key, req, ok := parseEchoRequest(pkt)
	if !ok {
		return false
	}
	if !ns.set.ICMP || ns.echoUnsupported.Load() {
		return true
	}
	flow := ns.echoFlow(key)
	select {
	case flow.queue <- req:
	default:
		slog.Debug("echo queue full, request dropped", "dst", key.dst, "seq", req.seq)
	}
	return true
}

func (ns *netStack) echoFlow(key echoKey) *echoFlow {
	ns.echoMu.Lock()
	defer ns.echoMu.Unlock()
	if f, ok := ns.echoFlows[key]; ok {
		return f
	}
	f := &echoFlow{key: key, queue: make(chan echoRequest, echoQueueLen)}
	ns.echoFlows[key] = f
	go ns.runEcho(f)
	return f
}

func (ns *netStack) dropEchoFlow(f *echoFlow) {
	ns.echoMu.Lock()
	if ns.echoFlows[f.key] == f {
		delete(ns.echoFlows, f.key)
	}
	ns.echoMu.Unlock()
}

func (ns *netStack) closeEchoFlows() {
	ns.echoMu.Lock()
	flows := make([]*echoFlow, 0, len(ns.echoFlows))
	for _, f := range ns.echoFlows {
		flows = append(flows, f)
	}
	ns.echoFlows = map[echoKey]*echoFlow{}
	ns.echoMu.Unlock()
	for _, f := range flows {
		f.mu.Lock()
		conn := f.conn
		f.mu.Unlock()
		if conn != nil {
			_ = conn.Close()
		}
	}
}

// runEcho is the goroutine of one flow. It ends on the idle timeout of the
// flow, on a helper close, or when the netstack closes.
func (ns *netStack) runEcho(f *echoFlow) {
	defer ns.dropEchoFlow(f)
	conn, err := ns.dialer.DialICMP(f.key.dst, f.key.ident)
	if err != nil {
		slog.Debug("icmp dial failed", "dst", f.key.dst, "error", err)
		return
	}
	f.mu.Lock()
	f.conn = conn
	f.mu.Unlock()
	defer func() { _ = conn.Close() }()

	var once sync.Once
	done := make(chan struct{})
	finish := func() { once.Do(func() { close(done) }) }

	go func() {
		defer finish()
		for {
			select {
			case req := <-f.queue:
				f.setSrc(req.src)
				if err := conn.WriteRequest(req.seq, req.ttl, req.payload); err != nil {
					return
				}
			case <-done:
				return
			}
		}
	}()
	go func() {
		defer finish()
		_ = conn.SetReadDeadline(time.Now().Add(udpIdle))
		if err := conn.ReadStatus(); err != nil {
			if errors.Is(err, mux.ErrEchoUnsupported) {
				ns.echoUnsupported.Store(true)
				ns.echoWarn.Do(func() {
					slog.Warn("the remote has no socket for icmp echo, ping through the session gets no reply; see tj doctor")
				})
			} else {
				slog.Debug("icmp echo status", "dst", f.key.dst, "error", err)
			}
			return
		}
		for {
			_ = conn.SetReadDeadline(time.Now().Add(udpIdle))
			reply, err := conn.ReadReply()
			if err != nil {
				return
			}
			src := f.source()
			if !src.IsValid() {
				continue
			}
			if reply.Error != nil {
				if reply.Error.From.Is4() != src.Is4() {
					continue
				}
				patchQuotedEchoIdent(reply.Error.Inner, f.key.ident, f.key.dst.Is6())
				inner := innerICMP(src, f.key.dst, reply.Error.Inner)
				ns.writePacket(buildICMPError(*reply.Error, src, inner))
				continue
			}
			slog.Debug("echo reply", "dst", f.key.dst, "seq", reply.Seq, "remote_rtt", reply.RTT)
			ns.writePacket(buildEchoReply(f.key.dst, src, f.key.ident, reply.Seq, reply.Payload))
		}
	}()
	<-done
}

// parseEchoRequest reads an IPv4 or IPv6 packet and returns the flow key
// and the request when the packet is an ICMP echo request. Every other
// packet, a fragment and a packet with an IPv6 extension header included,
// returns false. It copies the payload, because the pump reuses the buffer.
func parseEchoRequest(pkt []byte) (echoKey, echoRequest, bool) {
	if len(pkt) < 1 {
		return echoKey{}, echoRequest{}, false
	}
	switch pkt[0] >> 4 {
	case 4:
		h := header.IPv4(pkt)
		if !h.IsValid(len(pkt)) || h.Protocol() != uint8(header.ICMPv4ProtocolNumber) || h.More() || h.FragmentOffset() != 0 {
			return echoKey{}, echoRequest{}, false
		}
		icmp := header.ICMPv4(h.Payload())
		if len(icmp) < header.ICMPv4MinimumSize || icmp.Type() != header.ICMPv4Echo || icmp.Code() != 0 {
			return echoKey{}, echoRequest{}, false
		}
		return echoKey{dst: addrFrom(h.DestinationAddress()), ident: icmp.Ident()}, echoRequest{
			src:     addrFrom(h.SourceAddress()),
			seq:     icmp.Sequence(),
			ttl:     h.TTL(),
			payload: append([]byte(nil), icmp.Payload()...),
		}, true
	case 6:
		h := header.IPv6(pkt)
		if !h.IsValid(len(pkt)) || h.NextHeader() != uint8(header.ICMPv6ProtocolNumber) {
			return echoKey{}, echoRequest{}, false
		}
		icmp := header.ICMPv6(h.Payload())
		if len(icmp) < header.ICMPv6EchoMinimumSize || icmp.Type() != header.ICMPv6EchoRequest || icmp.Code() != 0 {
			return echoKey{}, echoRequest{}, false
		}
		return echoKey{dst: addrFrom(h.DestinationAddress()), ident: icmp.Ident()}, echoRequest{
			src:     addrFrom(h.SourceAddress()),
			seq:     icmp.Sequence(),
			ttl:     h.HopLimit(),
			payload: append([]byte(nil), icmp.Payload()...),
		}, true
	default:
		return echoKey{}, echoRequest{}, false
	}
}
