// Command datagram measures request-reply latency of DNS size over a QUIC
// connection in two modes, the stream mode that the tj UDP flow uses today
// and the datagram mode of RFC 9221, so chunk 4 of K8S-206 can decide
// whether the datagram path is worth building. It runs the server and the
// client in one process over 127.0.0.1 and is shaped with tc netem on lo.
// It is spike code for docs/spikes/07-udp-datagrams.md and no part of tj.
package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/netip"
	"os"
	"sort"
	"time"

	quic "github.com/apernet/quic-go"

	"github.com/evil8io/tailjump/internal/congestion"
	"github.com/evil8io/tailjump/internal/transport"
)

// The wire format of the stream mode is the tj UDP flow: the stream kind,
// the destination, and 2-byte length frames. The datagram mode adds a
// session id and a packet id, as Hysteria2 does.
const (
	kindUDP byte = 2

	datagramHdr = 12
)

const (
	quicPacketSize   = 1232
	quicIdleTimeout  = 30 * time.Second
	quicKeepAlive    = 10 * time.Second
	quicMaxStreams   = 1 << 16
	quicStreamWindow = 8 << 20
	quicConnWindow   = 20 << 20
	quicALPN         = "tj/2"
)

type config struct {
	n       int
	runs    int
	req     int
	reply   int
	timeout time.Duration
	tries   int
	modes   string
	label   string
}

func main() {
	var cfg config
	flag.IntVar(&cfg.n, "n", 200, "requests per run")
	flag.IntVar(&cfg.runs, "runs", 3, "runs per mode")
	flag.IntVar(&cfg.req, "req", 60, "request payload bytes")
	flag.IntVar(&cfg.reply, "reply", 120, "reply payload bytes")
	flag.DurationVar(&cfg.timeout, "timeout", 2*time.Second, "client timeout per attempt")
	flag.IntVar(&cfg.tries, "tries", 2, "attempts per request")
	flag.StringVar(&cfg.modes, "modes", "stream datagram", "modes to run")
	flag.StringVar(&cfg.label, "label", "", "condition label for the result lines")
	flag.Parse()

	if err := run(cfg); err != nil {
		fmt.Fprintln(os.Stderr, "datagram:", err)
		os.Exit(1)
	}
}

func run(cfg config) error {
	srvCert, srvFP, err := newCertificate()
	if err != nil {
		return err
	}
	cliCert, cliFP, err := newCertificate()
	if err != nil {
		return err
	}
	pc, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		return err
	}
	defer func() { _ = pc.Close() }()
	ln, err := quic.Listen(pc, serverTLS(srvCert, cliFP), quicConfig())
	if err != nil {
		return err
	}
	defer func() { _ = ln.Close() }()
	go serve(ln, cfg.reply)

	addr := netip.MustParseAddrPort(pc.LocalAddr().String())
	for _, mode := range splitModes(cfg.modes) {
		var p50s, p95s, overs []float64
		for i := 1; i <= cfg.runs; i++ {
			res, err := measure(mode, addr, cliCert, srvFP, cfg)
			if err != nil {
				return fmt.Errorf("%s run %d: %w", mode, i, err)
			}
			fmt.Printf("RESULT label=%q mode=%s run=%d n=%d ok=%d retried=%d p50_ms=%.3f p95_ms=%.3f max_ms=%.3f over2s=%d\n",
				cfg.label, mode, i, cfg.n, res.ok, res.retried, res.p50, res.p95, res.max, res.over2s)
			p50s = append(p50s, res.p50)
			p95s = append(p95s, res.p95)
			overs = append(overs, float64(res.over2s))
		}
		fmt.Printf("SUMMARY label=%q mode=%s runs=%d p50_ms=%.3f p95_ms=%.3f over2s=%.0f\n",
			cfg.label, mode, cfg.runs, medianOf(p50s), medianOf(p95s), medianOf(overs))
	}
	return nil
}

func splitModes(s string) []string {
	var out []string
	for _, m := range []string{"stream", "datagram"} {
		for _, f := range fields(s) {
			if f == m {
				out = append(out, m)
			}
		}
	}
	return out
}

func fields(s string) []string {
	var out []string
	start := -1
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ' ' || s[i] == ',' {
			if start >= 0 {
				out = append(out, s[start:i])
				start = -1
			}
			continue
		}
		if start < 0 {
			start = i
		}
	}
	return out
}

type result struct {
	ok      int
	retried int
	over2s  int
	p50     float64
	p95     float64
	max     float64
}

// measure dials a fresh connection and sends cfg.n requests one at a time.
// The latency of a request is the time from its first send to the reply, so a
// request that needed the retry carries the timeout, as a DNS client sees it.
func measure(mode string, addr netip.AddrPort, cert tls.Certificate, serverFP string, cfg config) (result, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pc, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		return result{}, err
	}
	conn, err := quic.Dial(ctx, pc, net.UDPAddrFromAddrPort(addr), clientTLS(cert, serverFP), quicConfig())
	if err != nil {
		_ = pc.Close()
		return result{}, err
	}
	defer func() {
		_ = conn.CloseWithError(0, "")
		_ = pc.Close()
	}()
	if err := congestion.Apply(conn, transport.Controller{Name: transport.BBR}); err != nil {
		return result{}, err
	}
	if ds := conn.ConnectionState().SupportsDatagrams; mode == "datagram" && (!ds.Local || !ds.Remote) {
		return result{}, errors.New("the peer does not support datagrams")
	}

	payload := make([]byte, cfg.req)
	var res result
	var ms []float64
	for i := 0; i < cfg.n; i++ {
		start := time.Now()
		var attempts int
		var ok bool
		for attempts = 1; attempts <= cfg.tries; attempts++ {
			if mode == "stream" {
				ok = streamRequest(conn, payload, cfg)
			} else {
				ok = datagramRequest(conn, uint32(i), payload, cfg)
			}
			if ok {
				break
			}
		}
		elapsed := float64(time.Since(start).Microseconds()) / 1000
		if attempts > 1 && ok {
			res.retried++
		}
		if !ok {
			continue
		}
		res.ok++
		if elapsed > 2000 {
			res.over2s++
		}
		ms = append(ms, elapsed)
	}
	sort.Float64s(ms)
	res.p50 = percentile(ms, 50)
	res.p95 = percentile(ms, 95)
	res.max = percentile(ms, 100)
	return res, nil
}

// streamRequest opens one bidirectional stream, writes the destination and
// one frame, and reads one frame, which is what the tj UDP flow does per
// 4-tuple.
func streamRequest(conn *quic.Conn, payload []byte, cfg config) bool {
	ctx, cancel := context.WithTimeout(context.Background(), cfg.timeout)
	defer cancel()
	st, err := conn.OpenStreamSync(ctx)
	if err != nil {
		return false
	}
	defer func() {
		st.CancelWrite(0)
		st.CancelRead(0)
	}()
	_ = st.SetDeadline(time.Now().Add(cfg.timeout))
	buf := make([]byte, 0, 10+len(payload))
	buf = append(buf, kindUDP, 4, 127, 0, 0, 1, 0, 53)
	buf = binary.BigEndian.AppendUint16(buf, uint16(len(payload)))
	buf = append(buf, payload...)
	if _, err := st.Write(buf); err != nil {
		return false
	}
	var hdr [2]byte
	if _, err := io.ReadFull(st, hdr[:]); err != nil {
		return false
	}
	reply := make([]byte, binary.BigEndian.Uint16(hdr[:]))
	if _, err := io.ReadFull(st, reply); err != nil {
		return false
	}
	return len(reply) == cfg.reply
}

// datagramRequest sends one datagram with a session id and a packet id and
// waits for the reply datagram with the same ids, as Hysteria2 does. It
// skips a late reply of an earlier attempt.
func datagramRequest(conn *quic.Conn, id uint32, payload []byte, cfg config) bool {
	ctx, cancel := context.WithTimeout(context.Background(), cfg.timeout)
	defer cancel()
	buf := make([]byte, datagramHdr+len(payload))
	binary.BigEndian.PutUint64(buf[:8], 1)
	binary.BigEndian.PutUint32(buf[8:12], id)
	copy(buf[datagramHdr:], payload)
	if err := conn.SendDatagram(buf); err != nil {
		return false
	}
	for {
		p, err := conn.ReceiveDatagram(ctx)
		if err != nil {
			return false
		}
		if len(p) != datagramHdr+cfg.reply {
			continue
		}
		if binary.BigEndian.Uint32(p[8:12]) == id {
			return true
		}
	}
}

// serve accepts connections and answers every stream and every datagram with
// a reply of the configured size.
func serve(ln *quic.Listener, replySize int) {
	for {
		conn, err := ln.Accept(context.Background())
		if err != nil {
			return
		}
		if err := congestion.Apply(conn, transport.Controller{Name: transport.BBR}); err != nil {
			fmt.Fprintln(os.Stderr, "datagram: server congestion:", err)
		}
		go serveStreams(conn, replySize)
		go serveDatagrams(conn, replySize)
	}
}

func serveStreams(conn *quic.Conn, replySize int) {
	for {
		st, err := conn.AcceptStream(context.Background())
		if err != nil {
			return
		}
		go func() {
			defer func() {
				_ = st.Close()
				st.CancelRead(0)
			}()
			var head [8]byte
			if _, err := io.ReadFull(st, head[:]); err != nil {
				return
			}
			var hdr [2]byte
			if _, err := io.ReadFull(st, hdr[:]); err != nil {
				return
			}
			if _, err := io.CopyN(io.Discard, st, int64(binary.BigEndian.Uint16(hdr[:]))); err != nil {
				return
			}
			reply := make([]byte, 2+replySize)
			binary.BigEndian.PutUint16(reply[:2], uint16(replySize))
			_, _ = st.Write(reply)
		}()
	}
}

func serveDatagrams(conn *quic.Conn, replySize int) {
	for {
		p, err := conn.ReceiveDatagram(context.Background())
		if err != nil {
			return
		}
		if len(p) < datagramHdr {
			continue
		}
		reply := make([]byte, datagramHdr+replySize)
		copy(reply[:datagramHdr], p[:datagramHdr])
		if err := conn.SendDatagram(reply); err != nil {
			return
		}
	}
}

// quicConfig is the configuration of internal/mux/quic.go with datagrams
// enabled, so the two modes differ in the flow protocol only.
func quicConfig() *quic.Config {
	return &quic.Config{
		MaxIdleTimeout:                 quicIdleTimeout,
		KeepAlivePeriod:                quicKeepAlive,
		InitialPacketSize:              quicPacketSize,
		DisablePathMTUDiscovery:        true,
		MaxIncomingStreams:             quicMaxStreams,
		MaxIncomingUniStreams:          quicMaxStreams,
		Allow0RTT:                      false,
		EnableDatagrams:                true,
		InitialStreamReceiveWindow:     quicStreamWindow,
		MaxStreamReceiveWindow:         quicStreamWindow,
		InitialConnectionReceiveWindow: quicConnWindow,
		MaxConnectionReceiveWindow:     quicConnWindow,
	}
}

func serverTLS(cert tls.Certificate, clientFP string) *tls.Config {
	return &tls.Config{
		Certificates:          []tls.Certificate{cert},
		ClientAuth:            tls.RequireAnyClientCert,
		VerifyPeerCertificate: verifyPinned(clientFP),
		NextProtos:            []string{quicALPN},
		MinVersion:            tls.VersionTLS13,
	}
}

func clientTLS(cert tls.Certificate, serverFP string) *tls.Config {
	return &tls.Config{
		Certificates:          []tls.Certificate{cert},
		InsecureSkipVerify:    true,
		VerifyPeerCertificate: verifyPinned(serverFP),
		NextProtos:            []string{quicALPN},
		MinVersion:            tls.VersionTLS13,
	}
}

func verifyPinned(want string) func([][]byte, [][]*x509.Certificate) error {
	return func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
		if len(rawCerts) == 0 {
			return errors.New("no peer certificate")
		}
		sum := sha256.Sum256(rawCerts[0])
		if hex.EncodeToString(sum[:]) != want {
			return errors.New("peer certificate fingerprint mismatch")
		}
		return nil
	}
}

func newCertificate() (tls.Certificate, string, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	if err != nil {
		return tls.Certificate{}, "", err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "tj-spike"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	sum := sha256.Sum256(der)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, hex.EncodeToString(sum[:]), nil
}

// percentile returns the nearest-rank percentile of sorted values, as the
// bench tool of the loss job does.
func percentile(sorted []float64, p int) float64 {
	if len(sorted) == 0 {
		return 0
	}
	rank := (p*len(sorted) + 99) / 100
	if rank < 1 {
		rank = 1
	}
	return sorted[rank-1]
}

func medianOf(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	s := append([]float64(nil), values...)
	sort.Float64s(s)
	mid := len(s) / 2
	if len(s)%2 == 1 {
		return s[mid]
	}
	return (s[mid-1] + s[mid]) / 2
}
