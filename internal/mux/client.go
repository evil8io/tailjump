package mux

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strings"
	"time"

	"github.com/hashicorp/yamux"
)

// Client is the client side of the mux. The caller opens one Client per
// session over the helper's stdin and stdout, then dials TCP and UDP flows.
type Client struct {
	sess *yamux.Session
	ctl  *yamux.Stream
	info ControlInfo
}

var _ Dialer = (*Client)(nil)

// NewClient reads the handshake, starts yamux over the transport, opens the
// control stream, and reads the helper's info line. It wraps the transport in
// one bufio.Reader for both the handshake and the yamux session, so no byte
// read past the handshake newline is lost to yamux.
func NewClient(transport io.ReadWriteCloser) (*Client, error) {
	reader := bufio.NewReader(transport)
	line, err := readHandshake(reader, handshakeTimeout)
	if err != nil {
		return nil, err
	}
	if line != handshakeLine {
		return nil, fmt.Errorf("remote helper: %s", line)
	}

	conn := &bufTransport{reader: reader, writer: transport, closer: transport}
	sess, err := yamux.Client(conn, muxConfig())
	if err != nil {
		return nil, err
	}
	ctl, err := sess.OpenStream()
	if err != nil {
		_ = sess.Close()
		return nil, err
	}
	if _, err := ctl.Write([]byte{kindControl}); err != nil {
		_ = sess.Close()
		return nil, err
	}
	infoLine, err := readLine(ctl, maxLineLen)
	if err != nil {
		_ = sess.Close()
		return nil, fmt.Errorf("read control info: %w", err)
	}
	info, err := decodeControlInfo(infoLine)
	if err != nil {
		_ = sess.Close()
		return nil, err
	}
	return &Client{sess: sess, ctl: ctl, info: info}, nil
}

// Info returns the helper's control info.
func (c *Client) Info() ControlInfo {
	return c.info
}

// Wait returns a channel that closes when the session ends, for example when a
// keepalive fails. It is the liveness check of the session.
func (c *Client) Wait() <-chan struct{} {
	return c.sess.CloseChan()
}

// Quit asks the helper to exit. The helper acts on quit at once and tears the
// session down, so the write can return a shutdown error before it returns
// success. That error means quit arrived, so it is success, not a failure.
func (c *Client) Quit() error {
	if _, err := c.ctl.Write([]byte(verbQuit + "\n")); err != nil && !sessionGone(err) {
		return err
	}
	return nil
}

// NegotiateQUIC asks the helper to start its QUIC listener and returns the
// port and the helper's certificate fingerprint. A helper that cannot listen
// answers with an *UnavailableError.
func (c *Client) NegotiateQUIC(req QUICRequest) (QUICReply, error) {
	if _, err := c.ctl.Write(req.encode()); err != nil {
		return QUICReply{}, fmt.Errorf("quic request: %w", err)
	}
	_ = c.ctl.SetReadDeadline(time.Now().Add(quicReplyTimeout))
	defer func() { _ = c.ctl.SetReadDeadline(time.Time{}) }()
	line, err := readLine(c.ctl, maxLineLen)
	if err != nil {
		return QUICReply{}, fmt.Errorf("quic reply: %w", err)
	}
	return parseQUICReply(line)
}

// AbandonQUIC tells the helper to close its listener, after a dial that did
// not complete. The session then runs on the SSH transport.
func (c *Client) AbandonQUIC() error {
	if _, err := c.ctl.Write([]byte(verbAbandon + "\n")); err != nil && !sessionGone(err) {
		return err
	}
	return nil
}

// sessionGone reports whether err means the session or its transport is
// already closed, which is the expected outcome of a delivered quit.
func sessionGone(err error) bool {
	return errors.Is(err, yamux.ErrSessionShutdown) ||
		errors.Is(err, yamux.ErrStreamClosed) ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrClosedPipe) ||
		errors.Is(err, net.ErrClosed)
}

// Close ends the session and every stream.
func (c *Client) Close() error {
	return c.sess.Close()
}

// DialTCP opens a TCP stream to the destination and waits for the helper's
// status byte. The returned net.Conn is the stream. A Close is a half-close.
func (c *Client) DialTCP(dst netip.AddrPort) (net.Conn, error) {
	stream, err := c.openStream(kindTCP, dst)
	if err != nil {
		return nil, err
	}
	status, err := readStatus(stream)
	if err != nil {
		_ = stream.Close()
		return nil, err
	}
	if status != statusOK {
		_ = stream.Close()
		return nil, statusError(status)
	}
	return stream, nil
}

// UDPConn is a UDP flow over one stream. Frames carry one datagram each.
type UDPConn struct {
	stream Stream
}

// DialUDP opens a UDP stream to the destination. It does not wait for a status
// byte, so the caller can send the first datagram at once and save a round
// trip. A dial error on the helper closes the stream, which ReadFrame reports
// as the end of the flow.
func (c *Client) DialUDP(dst netip.AddrPort) (*UDPConn, error) {
	stream, err := c.openStream(kindUDP, dst)
	if err != nil {
		return nil, err
	}
	return &UDPConn{stream: stream}, nil
}

func (c *Client) openStream(kind byte, dst netip.AddrPort) (*yamux.Stream, error) {
	stream, err := c.sess.OpenStream()
	if err != nil {
		return nil, err
	}
	if _, err := stream.Write([]byte{kind}); err != nil {
		_ = stream.Close()
		return nil, err
	}
	if err := writeDest(stream, dst); err != nil {
		_ = stream.Close()
		return nil, err
	}
	return stream, nil
}

// WriteFrame sends one datagram.
func (u *UDPConn) WriteFrame(p []byte) error {
	return writeFrame(u.stream, p)
}

// ReadFrame reads one datagram. It returns an error when the helper closes the
// flow, for example on its idle timeout or on a dial error.
func (u *UDPConn) ReadFrame() ([]byte, error) {
	return readFrame(u.stream)
}

// SetReadDeadline sets the deadline for ReadFrame.
func (u *UDPConn) SetReadDeadline(t time.Time) error {
	return u.stream.SetReadDeadline(t)
}

// Close ends the flow.
func (u *UDPConn) Close() error {
	return u.stream.Close()
}

func readHandshake(reader *bufio.Reader, timeout time.Duration) (string, error) {
	type result struct {
		line string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		line, err := reader.ReadString('\n')
		ch <- result{line, err}
	}()
	select {
	case res := <-ch:
		if res.err != nil {
			return "", res.err
		}
		return strings.TrimRight(res.line, "\r\n"), nil
	case <-time.After(timeout):
		return "", fmt.Errorf("mux: no handshake within %s", timeout)
	}
}

// bufTransport reads through the bufio.Reader that consumed the handshake and
// writes and closes the original transport.
type bufTransport struct {
	reader *bufio.Reader
	writer io.Writer
	closer io.Closer
}

func (t *bufTransport) Read(p []byte) (int, error)  { return t.reader.Read(p) }
func (t *bufTransport) Write(p []byte) (int, error) { return t.writer.Write(p) }
func (t *bufTransport) Close() error                { return t.closer.Close() }
