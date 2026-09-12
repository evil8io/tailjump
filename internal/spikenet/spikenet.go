// Package spikenet has the pieces the QUIC spike binaries share: the
// self-signed certificate, the raw TCP and UDP probe servers, the stream
// request encoding, and the measurement helpers. It is throwaway spike code
// for K8S-208 and it is not part of the tj build.
package spikenet

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"sort"
	"sync/atomic"
	"time"
)

// ALPN is the protocol name the spike client and server agree on.
const ALPN = "tj/2"

// Commands on a stream.
const (
	CmdDownload byte = 1
)

// SelfDelete removes the running binary, as the real helper does, so no file
// remains on a remote.
func SelfDelete() {
	if len(os.Args) > 0 && os.Args[0] != "" {
		_ = os.Remove(os.Args[0])
	}
}

// Certificate returns a fresh self-signed ECDSA P-256 certificate.
func Certificate() (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "tj-spike"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, nil
}

// ServerTLS returns the server TLS config with a fresh certificate.
func ServerTLS() (*tls.Config, error) {
	cert, err := Certificate()
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		NextProtos:   []string{ALPN},
		MinVersion:   tls.VersionTLS13,
	}, nil
}

// ClientTLS returns the client TLS config. The spike does not pin, because
// pinning is chunk 2 and it does not change a measurement.
func ClientTLS() *tls.Config {
	return &tls.Config{
		InsecureSkipVerify: true, //nolint:gosec // spike client, pinning is chunk 2
		NextProtos:         []string{ALPN},
		MinVersion:         tls.VersionTLS13,
	}
}

// WriteRequest writes the command byte and the byte count.
func WriteRequest(w io.Writer, cmd byte, n uint64) error {
	buf := make([]byte, 9)
	buf[0] = cmd
	binary.BigEndian.PutUint64(buf[1:], n)
	_, err := w.Write(buf)
	return err
}

// ReadRequest reads the command byte and the byte count.
func ReadRequest(r io.Reader) (byte, uint64, error) {
	buf := make([]byte, 9)
	if _, err := io.ReadFull(r, buf); err != nil {
		return 0, 0, err
	}
	return buf[0], binary.BigEndian.Uint64(buf[1:]), nil
}

// WriteZeros writes n zero bytes in 64 KiB chunks.
func WriteZeros(w io.Writer, n uint64) error {
	block := make([]byte, 64<<10)
	for n > 0 {
		c := uint64(len(block))
		if n < c {
			c = n
		}
		if _, err := w.Write(block[:c]); err != nil {
			return err
		}
		n -= c
	}
	return nil
}

// Drain reads r to EOF and returns the byte count.
func Drain(r io.Reader) (int64, error) {
	return io.Copy(io.Discard, r)
}

// ServeTCP serves the download command on a TCP listener.
func ServeTCP(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			defer c.Close()
			cmd, n, err := ReadRequest(c)
			if err != nil || cmd != CmdDownload {
				return
			}
			_ = WriteZeros(c, n)
		}(c)
	}
}

// ServeUDPProbe answers every datagram with the observed payload size in
// decimal, so the client learns which probe sizes cross the path.
func ServeUDPProbe(c *net.UDPConn) {
	buf := make([]byte, 65535)
	for {
		n, addr, err := c.ReadFromUDP(buf)
		if err != nil {
			return
		}
		_, _ = c.WriteToUDP([]byte(fmt.Sprintf("got %d", n)), addr)
	}
}

// SizeConn wraps a net.PacketConn and records the largest datagram written and
// read, which is how the spike observes the QUIC packet size on the wire.
type SizeConn struct {
	net.PacketConn
	MaxWrite atomic.Int64
	MaxRead  atomic.Int64
}

func (s *SizeConn) WriteTo(p []byte, addr net.Addr) (int, error) {
	if int64(len(p)) > s.MaxWrite.Load() {
		s.MaxWrite.Store(int64(len(p)))
	}
	return s.PacketConn.WriteTo(p, addr)
}

func (s *SizeConn) ReadFrom(p []byte) (int, net.Addr, error) {
	n, addr, err := s.PacketConn.ReadFrom(p)
	if int64(n) > s.MaxRead.Load() {
		s.MaxRead.Store(int64(n))
	}
	return n, addr, err
}

// Mbits returns the rate of n bytes in d as Mbit/s.
func Mbits(n int64, d time.Duration) float64 {
	return float64(n) * 8 / d.Seconds() / 1e6
}

// Median returns the median of the values. It sorts a copy.
func Median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	c := append([]float64(nil), v...)
	sort.Float64s(c)
	return c[len(c)/2]
}
