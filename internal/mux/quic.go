package mux

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/netip"
	"sync/atomic"
	"time"

	quic "github.com/apernet/quic-go"

	"github.com/evil8io/tailjump/internal/congestion"
	"github.com/evil8io/tailjump/internal/transport"
)

// quicConfig is the QUIC configuration of both sides. The packet size and
// the disabled path MTU discovery are mandatory: the tailnet path MTU is
// 1280, and spike 5 measured that the library default of 1280 bytes of
// payload completes no handshake at all. The receive windows start at their
// maximum, as Hysteria2 does; spike 6 measured 22% to 55% more throughput
// under 7% loss against the library defaults.
func quicConfig() *quic.Config {
	return &quic.Config{
		MaxIdleTimeout:                 quicIdleTimeout,
		KeepAlivePeriod:                quicKeepAlive,
		InitialPacketSize:              quicPacketSize,
		DisablePathMTUDiscovery:        true,
		MaxIncomingStreams:             quicMaxStreams,
		MaxIncomingUniStreams:          quicMaxStreams,
		Allow0RTT:                      false,
		EnableDatagrams:                false,
		InitialStreamReceiveWindow:     quicStreamWindow,
		MaxStreamReceiveWindow:         quicStreamWindow,
		InitialConnectionReceiveWindow: quicConnWindow,
		MaxConnectionReceiveWindow:     quicConnWindow,
	}
}

// QUICClient is the client side of the QUIC transport: one connection to the
// helper's listener, and one stream per flow. It implements Dialer.
type QUICClient struct {
	conn *quic.Conn
	pc   *net.UDPConn
	port uint16
}

var _ Dialer = (*QUICClient)(nil)

// NewClientCertificate returns the client's per-session certificate and its
// fingerprint, for the negotiation and the dial.
func NewClientCertificate() (tls.Certificate, string, error) {
	return newCertificate()
}

// DialQUIC dials the helper's listener at addr with the client certificate,
// pins the helper certificate by its fingerprint, and installs the
// controller. It fails when the handshake does not complete within 5 s.
func DialQUIC(ctx context.Context, addr netip.AddrPort, cert tls.Certificate, helperFP string, ctl transport.Controller) (*QUICClient, error) {
	pc, err := net.ListenUDP("udp", nil)
	if err != nil {
		return nil, fmt.Errorf("quic: udp socket: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, quicHandshakeTimeout)
	defer cancel()
	conn, err := quic.Dial(ctx, pc, net.UDPAddrFromAddrPort(addr), clientTLSConfig(cert, helperFP), quicConfig())
	if err != nil {
		_ = pc.Close()
		return nil, fmt.Errorf("quic: dial %s: %w", addr, err)
	}
	q := &QUICClient{conn: conn, pc: pc, port: addr.Port()}
	if err := congestion.Apply(conn, ctl); err != nil {
		_ = q.Close()
		return nil, err
	}
	if err := q.probe(ctx); err != nil {
		_ = q.Close()
		return nil, fmt.Errorf("quic: probe %s: %w", addr, err)
	}
	return q, nil
}

// probe opens a probe stream and waits for the helper's ok status. TLS 1.3
// lets the client complete the handshake before the helper has verified the
// client certificate, so the dial counts only when the helper answers.
func (q *QUICClient) probe(ctx context.Context) error {
	st, err := q.conn.OpenStreamSync(ctx)
	if err != nil {
		return err
	}
	stream := wrapStream(st, q.conn)
	defer stream.abort()
	if deadline, ok := ctx.Deadline(); ok {
		_ = stream.SetReadDeadline(deadline)
	}
	if _, err := stream.Write([]byte{kindProbe}); err != nil {
		return err
	}
	status, err := readStatus(stream)
	if err != nil {
		return err
	}
	if status != statusOK {
		return statusError(status)
	}
	return nil
}

// Port returns the helper's listener port.
func (q *QUICClient) Port() uint16 {
	return q.port
}

// Wait returns a channel that closes when the QUIC connection ends.
func (q *QUICClient) Wait() <-chan struct{} {
	return q.conn.Context().Done()
}

// Close ends the connection and every stream, and closes the UDP socket.
func (q *QUICClient) Close() error {
	err := q.conn.CloseWithError(0, "")
	_ = q.pc.Close()
	return err
}

// DialTCP opens a stream to the destination and waits for the helper's
// status byte. A Close on the returned conn is a half-close.
func (q *QUICClient) DialTCP(dst netip.AddrPort) (net.Conn, error) {
	stream, err := q.openStream(kindTCP, dst)
	if err != nil {
		return nil, err
	}
	status, err := readStatus(stream)
	if err != nil {
		stream.abort()
		return nil, err
	}
	if status != statusOK {
		stream.abort()
		return nil, statusError(status)
	}
	return stream, nil
}

// DialUDP opens a stream for a UDP flow. As on the SSH transport, it does
// not wait for a status byte.
func (q *QUICClient) DialUDP(dst netip.AddrPort) (*UDPConn, error) {
	stream, err := q.openStream(kindUDP, dst)
	if err != nil {
		return nil, err
	}
	return &UDPConn{stream: stream}, nil
}

// DialICMP opens a stream for an ICMP echo flow. As on the SSH transport, it
// does not wait for the status byte.
func (q *QUICClient) DialICMP(dst netip.Addr, ident uint16) (*EchoConn, error) {
	stream, err := q.openStream(kindICMP, netip.AddrPortFrom(dst, ident))
	if err != nil {
		return nil, err
	}
	return &EchoConn{stream: stream}, nil
}

func (q *QUICClient) openStream(kind byte, dst netip.AddrPort) (*quicStream, error) {
	ctx, cancel := context.WithTimeout(context.Background(), quicOpenTimeout)
	defer cancel()
	st, err := q.conn.OpenStreamSync(ctx)
	if err != nil {
		return nil, fmt.Errorf("quic: open stream: %w", err)
	}
	stream := wrapStream(st, q.conn)
	if _, err := stream.Write([]byte{kind}); err != nil {
		stream.abort()
		return nil, err
	}
	if err := writeDest(stream, dst); err != nil {
		stream.abort()
		return nil, err
	}
	return stream, nil
}

// quicStream adapts a QUIC stream to Stream and net.Conn with the half-close
// rule of the flow protocol: the first Close closes the send side, and the
// peer reads EOF; a second Close releases the read side with CancelRead.
type quicStream struct {
	st         *quic.Stream
	local      net.Addr
	remote     net.Addr
	sendClosed atomic.Bool
}

func wrapStream(st *quic.Stream, conn *quic.Conn) *quicStream {
	return &quicStream{st: st, local: conn.LocalAddr(), remote: conn.RemoteAddr()}
}

func (s *quicStream) Read(p []byte) (int, error)  { return s.st.Read(p) }
func (s *quicStream) Write(p []byte) (int, error) { return s.st.Write(p) }

func (s *quicStream) Close() error {
	if s.sendClosed.CompareAndSwap(false, true) {
		return s.st.Close()
	}
	s.st.CancelRead(0)
	return nil
}

// abort ends both directions at once, for a flow that never started.
func (s *quicStream) abort() {
	s.st.CancelWrite(0)
	s.st.CancelRead(0)
}

func (s *quicStream) LocalAddr() net.Addr                { return s.local }
func (s *quicStream) RemoteAddr() net.Addr               { return s.remote }
func (s *quicStream) SetDeadline(t time.Time) error      { return s.st.SetDeadline(t) }
func (s *quicStream) SetReadDeadline(t time.Time) error  { return s.st.SetReadDeadline(t) }
func (s *quicStream) SetWriteDeadline(t time.Time) error { return s.st.SetWriteDeadline(t) }
