package mux

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/hashicorp/yamux"
)

// Server is the helper side of the mux. It writes the handshake, serves yamux,
// dials the destinations, and relays TCP streams and UDP flows. It opens no
// listener. It imports no logging package so the helper binary stays small and
// links no encoding/json.
type Server struct {
	Info ControlInfo

	// DialTimeout is the TCP dial timeout. It defaults to 10s.
	DialTimeout time.Duration
	// UDPIdle is the UDP idle timeout for a non-DNS flow. It defaults to 60s.
	UDPIdle time.Duration
	// UDPIdleDNS is the UDP idle timeout for a flow to port 53. It defaults
	// to 10s.
	UDPIdleDNS time.Duration

	// LogW receives one line per dial error. A nil LogW disables logging.
	LogW io.Writer
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

func (s *Server) handle(stream *yamux.Stream, stop func()) {
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
	default:
		_ = stream.Close()
	}
}

func (s *Server) handleControl(stream *yamux.Stream, stop func()) {
	defer func() { _ = stream.Close() }()
	if _, err := stream.Write(s.Info.encode()); err != nil {
		return
	}
	for {
		line, err := readLine(stream, maxLineLen)
		if err != nil {
			return
		}
		if line == "quit" {
			stop()
			return
		}
	}
}

func (s *Server) handleTCP(stream *yamux.Stream) {
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

func relayTCP(stream *yamux.Stream, conn net.Conn) {
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

func (s *Server) handleUDP(stream *yamux.Stream) {
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
	// A UDP stream sends no success status byte. The client pipelines the
	// first datagram after the destination, so a status byte would need a
	// round trip and would sit inside the frame stream. On a dial error the
	// helper writes a non-zero status byte and closes, which the client sees
	// as the end of the flow.
	relayUDP(stream, conn, s.idleFor(dst))
}

func (s *Server) idleFor(dst netip.AddrPort) time.Duration {
	def := s.UDPIdle
	if def <= 0 {
		def = udpIdle
	}
	dns := s.UDPIdleDNS
	if dns <= 0 {
		dns = udpIdleDNS
	}
	return idleFor(dst.Port(), def, dns)
}

func relayUDP(stream *yamux.Stream, conn *net.UDPConn, idle time.Duration) {
	var closeOnce sync.Once
	closeAll := func() {
		closeOnce.Do(func() {
			_ = stream.Close()
			_ = conn.Close()
		})
	}

	var mu sync.Mutex
	timer := time.AfterFunc(idle, closeAll)
	defer timer.Stop()
	reset := func() {
		mu.Lock()
		timer.Reset(idle)
		mu.Unlock()
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		defer closeAll()
		for {
			p, err := readFrame(stream)
			if err != nil {
				return
			}
			reset()
			if _, err := conn.Write(p); err != nil {
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		defer closeAll()
		buf := make([]byte, maxUDPFrame)
		for {
			n, err := conn.Read(buf)
			if err != nil {
				return
			}
			reset()
			if err := writeFrame(stream, buf[:n]); err != nil {
				return
			}
		}
	}()
	wg.Wait()
}
