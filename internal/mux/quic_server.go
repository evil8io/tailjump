package mux

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"

	quic "github.com/apernet/quic-go"

	"github.com/evil8io/tailjump/internal/congestion"
	"github.com/evil8io/tailjump/internal/transport"
)

// quicServer is the helper's QUIC listener for one session. It accepts one
// pinned connection and feeds every stream to the flow handlers.
type quicServer struct {
	ln       *quic.Listener
	pc       *net.UDPConn
	port     uint16
	accepted atomic.Bool
	ctl      transport.Controller

	mu   sync.Mutex
	conn *quic.Conn
}

// startQUIC binds the first free port of the range on the bind address,
// starts the listener with a fresh certificate, and returns the reply for
// the control stream. The listener binds the tailnet address only, never
// the wildcard address.
func (s *Server) startQUIC(req QUICRequest) (QUICReply, error) {
	s.quicMu.Lock()
	defer s.quicMu.Unlock()
	if s.quic != nil {
		return QUICReply{}, errors.New("a quic listener already exists")
	}
	cert, fp, err := newCertificate()
	if err != nil {
		return QUICReply{}, fmt.Errorf("certificate: %w", err)
	}
	pc, port, err := bindInRange(req.BindAddr.AsSlice(), req.Ports)
	if err != nil {
		return QUICReply{}, err
	}
	q := &quicServer{pc: pc, port: port, ctl: req.Controller}
	ln, err := quic.Listen(pc, helperTLSConfig(cert, req.Fingerprint, &q.accepted), quicConfig())
	if err != nil {
		_ = pc.Close()
		return QUICReply{}, fmt.Errorf("listen: %w", err)
	}
	q.ln = ln
	s.quic = q
	go s.serveQUIC(q)
	return QUICReply{Port: port, Fingerprint: fp}, nil
}

func bindInRange(ip net.IP, ports transport.PortRange) (*net.UDPConn, uint16, error) {
	var lastErr error
	for p := int(ports.First); p <= int(ports.Last); p++ {
		pc, err := net.ListenUDP("udp", &net.UDPAddr{IP: ip, Port: p})
		if err == nil {
			return pc, uint16(p), nil
		}
		lastErr = err
	}
	return nil, 0, fmt.Errorf("no free udp port in %s on %s: %v", ports, ip, lastErr)
}

// stopQUIC closes the listener, which also closes the accepted connection,
// and the UDP socket. It is safe to call twice.
func (s *Server) stopQUIC() {
	s.quicMu.Lock()
	q := s.quic
	s.quic = nil
	s.quicMu.Unlock()
	if q == nil {
		return
	}
	q.mu.Lock()
	if q.conn != nil {
		_ = q.conn.CloseWithError(0, "")
	}
	q.mu.Unlock()
	_ = q.ln.Close()
	_ = q.pc.Close()
}

// serveQUIC accepts the one connection of the session and serves its
// streams until the connection or the listener closes.
func (s *Server) serveQUIC(q *quicServer) {
	conn, err := q.ln.Accept(context.Background())
	if err != nil {
		return
	}
	q.accepted.Store(true)
	q.mu.Lock()
	q.conn = conn
	q.mu.Unlock()
	if err := congestion.Apply(conn, q.ctl); err != nil {
		s.logf("quic congestion: %v", err)
	}
	for {
		st, err := conn.AcceptStream(context.Background())
		if err != nil {
			return
		}
		go s.handleFlow(wrapStream(st, conn))
	}
}
