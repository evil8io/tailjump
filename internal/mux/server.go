package mux

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/yamux"
)

// Server is the helper side of the mux. It writes the handshake, serves yamux,
// dials the destinations, and relays TCP streams, UDP flows, and ICMP echo
// flows. It opens no listener beyond the QUIC listener. It imports no
// logging package so the helper binary stays small and links no
// encoding/json.
type Server struct {
	Info ControlInfo

	// DialTimeout is the TCP dial timeout. It defaults to 10s.
	DialTimeout time.Duration
	// UDPIdle is the idle timeout for a non-DNS UDP flow and for an echo
	// flow. It defaults to 60s.
	UDPIdle time.Duration
	// UDPIdleDNS is the UDP idle timeout for a flow to port 53. It defaults
	// to 10s.
	UDPIdleDNS time.Duration

	// LogW receives one line per dial error. A nil LogW disables logging.
	LogW io.Writer

	// Unlink runs when the client sends the unlink verb. The helper sets it
	// to the remove of its own file, so this package needs no file logic. A
	// nil Unlink ignores the verb.
	Unlink func()

	quicMu sync.Mutex
	quic   *quicServer
}

// Serve writes the handshake, runs the mux over the transport, and returns
// when the client sends quit or the transport closes.
func (s *Server) Serve(transport io.ReadWriteCloser) error {
	if _, err := io.WriteString(transport, handshakeLine+"\n"); err != nil {
		return err
	}
	sess, err := yamux.Server(transport, muxConfig())
	if err != nil {
		return err
	}
	defer func() { _ = sess.Close() }()
	defer s.stopQUIC()

	done := make(chan struct{})
	var once sync.Once
	stop := func() { once.Do(func() { close(done) }) }

	go func() {
		for {
			stream, err := sess.AcceptStream()
			if err != nil {
				stop()
				return
			}
			go s.handle(stream, stop)
		}
	}()

	<-done
	return nil
}

func (s *Server) logf(format string, args ...any) {
	if s.LogW != nil {
		_, _ = fmt.Fprintf(s.LogW, format+"\n", args...)
	}
}

func (s *Server) handle(stream Stream, stop func()) {
	var kind [1]byte
	if _, err := io.ReadFull(stream, kind[:]); err != nil {
		_ = stream.Close()
		return
	}
	switch kind[0] {
	case kindControl:
		s.handleControl(stream, stop)
	case kindTCP:
		s.handleTCP(stream)
	case kindUDP:
		s.handleUDP(stream)
	case kindICMP:
		s.handleICMP(stream)
	default:
		_ = stream.Close()
	}
}

// handleFlow serves a stream of the QUIC transport. The control stream stays
// on the SSH transport, so kind 0 is refused here.
func (s *Server) handleFlow(stream Stream) {
	var kind [1]byte
	if _, err := io.ReadFull(stream, kind[:]); err != nil {
		_ = stream.Close()
		return
	}
	switch kind[0] {
	case kindTCP:
		s.handleTCP(stream)
	case kindUDP:
		s.handleUDP(stream)
	case kindICMP:
		s.handleICMP(stream)
	case kindProbe:
		s.handleProbe(stream)
	default:
		_ = stream.Close()
	}
}

// handleProbe answers the client's probe stream with the ok status. The
// client sends it right after the QUIC handshake, because TLS 1.3 lets the
// client finish before the helper has verified the client certificate, so
// only an answered probe proves that the helper accepted the connection.
func (s *Server) handleProbe(stream Stream) {
	_, _ = stream.Write([]byte{statusOK})
	_ = stream.Close()
	_ = stream.Close()
}

func (s *Server) handleControl(stream Stream, stop func()) {
	defer func() { _ = stream.Close() }()
	if _, err := stream.Write(s.Info.encode()); err != nil {
		return
	}
	for {
		line, err := readLine(stream, maxLineLen)
		if err != nil {
			return
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		switch fields[0] {
		case verbQuit:
			s.stopQUIC()
			stop()
			return
		case verbQUIC:
			if _, err := stream.Write(s.answerQUIC(fields[1:])); err != nil {
				return
			}
		case verbAbandon:
			s.stopQUIC()
		case verbICMP:
			if _, err := stream.Write(probeEchoSocket().encode()); err != nil {
				return
			}
		case verbUnlink:
			if s.Unlink != nil {
				s.Unlink()
			}
		}
	}
}

// answerQUIC starts the listener for a quic request and returns the reply
// line, or the unavailable line with the reason.
func (s *Server) answerQUIC(fields []string) []byte {
	req, err := parseQUICRequest(fields)
	if err != nil {
		return encodeUnavailable(err.Error())
	}
	reply, err := s.startQUIC(req)
	if err != nil {
		s.logf("quic listener: %v", err)
		return encodeUnavailable(err.Error())
	}
	return reply.encode()
}

func (s *Server) handleTCP(stream Stream) {
	defer func() { _ = stream.Close() }()
	dst, err := readDest(stream)
	if err != nil {
		return
	}
	timeout := s.DialTimeout
	if timeout <= 0 {
		timeout = dialTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", dst.String())
	if err != nil {
		s.logf("tcp dial %s: %v", dst, err)
		_, _ = stream.Write([]byte{statusFor(err)})
		return
	}
	defer func() { _ = conn.Close() }()
	if _, err := stream.Write([]byte{statusOK}); err != nil {
		return
	}
	relayTCP(stream, conn)
}

type closeWriter interface {
	CloseWrite() error
}

func relayTCP(stream Stream, conn net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(conn, stream)
		if cw, ok := conn.(closeWriter); ok {
			_ = cw.CloseWrite()
		} else {
			_ = conn.Close()
		}
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(stream, conn)
		_ = stream.Close()
	}()
	wg.Wait()
}

func (s *Server) handleUDP(stream Stream) {
	defer func() { _ = stream.Close() }()
	dst, err := readDest(stream)
	if err != nil {
		return
	}
	conn, err := net.DialUDP("udp", nil, net.UDPAddrFromAddrPort(dst))
	if err != nil {
		s.logf("udp dial %s: %v", dst, err)
		_, _ = stream.Write([]byte{statusFor(err)})
		return
	}
	defer func() { _ = conn.Close() }()
	v6 := dst.Addr().Unmap().Is6()
	_ = enableRecvErr(conn, v6)
	// A UDP stream sends no success status byte. The client pipelines the
	// first datagram after the destination, so a status byte would need a
	// round trip and would sit inside the frame stream. On a dial error the
	// helper writes a non-zero status byte and closes, which the client sees
	// as the end of the flow.
	relayUDP(stream, conn, v6, s.idleFor(dst))
}

func (s *Server) idleFor(dst netip.AddrPort) time.Duration {
	dns := s.UDPIdleDNS
	if dns <= 0 {
		dns = udpIdleDNS
	}
	return idleFor(dst.Port(), s.udpIdle(), dns)
}

func (s *Server) udpIdle() time.Duration {
	if s.UDPIdle > 0 {
		return s.UDPIdle
	}
	return udpIdle
}

// flowTimer closes a flow after an idle time, and every frame resets it.
type flowTimer struct {
	mu    sync.Mutex
	timer *time.Timer
	idle  time.Duration
}

func newFlowTimer(idle time.Duration, closeAll func()) *flowTimer {
	return &flowTimer{timer: time.AfterFunc(idle, closeAll), idle: idle}
}

func (t *flowTimer) reset() {
	t.mu.Lock()
	t.timer.Reset(t.idle)
	t.mu.Unlock()
}

func (t *flowTimer) stop() {
	t.mu.Lock()
	t.timer.Stop()
	t.mu.Unlock()
}

// relayUDP copies datagrams both ways. A request frame carries the TTL of
// the captured datagram, which the helper sets on the socket. An ICMP error
// for a sent datagram comes back as an error frame and the flow continues,
// so a traceroute probe gets its Time Exceeded and a probe to a closed port
// gets its Port Unreachable.
func relayUDP(stream Stream, conn *net.UDPConn, v6 bool, idle time.Duration) {
	var closeOnce sync.Once
	closeAll := func() {
		closeOnce.Do(func() {
			_ = stream.Close()
			_ = conn.Close()
		})
	}
	timer := newFlowTimer(idle, closeAll)
	defer timer.stop()

	// Both goroutines write to the stream, and a QUIC stream write is not
	// safe to interleave, so the writes share a lock.
	var wmu sync.Mutex
	write := func(frame []byte) error {
		wmu.Lock()
		defer wmu.Unlock()
		return writeFrame(stream, frame)
	}
	writeErrors := func(errs []ICMPError) error {
		for _, e := range errs {
			if err := write(encodeUDPError(e)); err != nil {
				return err
			}
		}
		return nil
	}

	ttl := ttlSetter{conn: conn, v6: v6}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		defer closeAll()
		for {
			frame, err := readFrame(stream)
			if err != nil {
				return
			}
			hops, payload, err := decodeUDPRequest(frame)
			if err != nil {
				return
			}
			timer.reset()
			_ = ttl.set(hops)
			if _, err := conn.Write(payload); err != nil {
				errs := icmpErrorsFor(conn, v6, err)
				if errs == nil {
					return
				}
				if err := writeErrors(errs); err != nil {
					return
				}
			}
		}
	}()
	go func() {
		defer wg.Done()
		defer closeAll()
		buf := make([]byte, maxUDPFrame)
		emptyDrains := 0
		for {
			n, err := conn.Read(buf)
			if err != nil {
				errs := icmpErrorsFor(conn, v6, err)
				if errs == nil {
					emptyDrains++
					if !isICMPErrno(err) || emptyDrains > 8 {
						return
					}
					continue
				}
				emptyDrains = 0
				timer.reset()
				if err := writeErrors(errs); err != nil {
					return
				}
				continue
			}
			timer.reset()
			if err := write(encodeUDPData(buf[:n])); err != nil {
				return
			}
		}
	}()
	wg.Wait()
}

// handleICMP serves an ICMP echo flow. The destination's port field is the
// identifier. The helper writes the status byte first: ok, or unsupported
// when it has no echo socket. The idle timeout of a UDP flow applies.
func (s *Server) handleICMP(stream Stream) {
	defer func() { _ = stream.Close() }()
	dst, err := readDest(stream)
	if err != nil {
		return
	}
	sock, err := openEchoSocket(dst.Addr().Unmap().Is6())
	if err != nil {
		s.logf("icmp echo %s: %v", dst.Addr(), err)
		_, _ = stream.Write([]byte{statusUnsupported})
		return
	}
	defer func() { _ = sock.Close() }()
	if _, err := stream.Write([]byte{statusOK}); err != nil {
		return
	}
	relayEcho(stream, sock, dst.Addr().Unmap(), s.udpIdle())
}

// relayEcho sends every request frame as an echo request with its TTL, and
// returns each reply and each ICMP error as a reply frame.
func relayEcho(stream Stream, sock *echoSocket, dst netip.Addr, idle time.Duration) {
	var closeOnce sync.Once
	closeAll := func() {
		closeOnce.Do(func() {
			_ = stream.Close()
			_ = sock.Close()
		})
	}
	timer := newFlowTimer(idle, closeAll)
	defer timer.stop()

	var wmu sync.Mutex
	write := func(frame []byte) error {
		wmu.Lock()
		defer wmu.Unlock()
		return writeFrame(stream, frame)
	}

	var times echoTimes
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		defer closeAll()
		for {
			frame, err := readFrame(stream)
			if err != nil {
				return
			}
			seq, ttl, payload, err := decodeEchoRequest(frame)
			if err != nil {
				return
			}
			timer.reset()
			times.mark(seq)
			errs, err := sock.send(dst, seq, ttl, payload)
			if err != nil {
				return
			}
			for _, e := range errs {
				if err := write(encodeEchoError(e)); err != nil {
					return
				}
			}
		}
	}()
	go func() {
		defer wg.Done()
		defer closeAll()
		buf := make([]byte, maxUDPFrame)
		for {
			ev, err := sock.recv(buf)
			if err != nil {
				return
			}
			timer.reset()
			var frame []byte
			if ev.err != nil {
				frame = encodeEchoError(*ev.err)
			} else {
				frame = encodeEchoData(ev.seq, times.take(ev.seq), ev.payload)
			}
			if err := write(frame); err != nil {
				return
			}
		}
	}()
	wg.Wait()
}
