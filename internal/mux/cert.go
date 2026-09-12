package mux

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"errors"
	"math/big"
	"sync/atomic"
	"time"
)

const fingerprintPrefix = "sha256:"

// newCertificate returns a fresh self-signed ECDSA P-256 certificate, in
// memory only, and its fingerprint. Each side makes one per session.
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
		Subject:      pkix.Name{CommonName: "tj"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, fingerprint(der), nil
}

func fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return fingerprintPrefix + hex.EncodeToString(sum[:])
}

// verifyPinned returns a VerifyPeerCertificate function that accepts only
// the certificate with the pinned fingerprint. The chain check is off on
// both sides, because the pin exchanged over the authenticated SSH channel
// is the trust anchor.
func verifyPinned(want string) func([][]byte, [][]*x509.Certificate) error {
	return func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
		if len(rawCerts) == 0 {
			return errors.New("no peer certificate")
		}
		if fingerprint(rawCerts[0]) != want {
			return errors.New("peer certificate fingerprint mismatch")
		}
		return nil
	}
}

// helperTLSConfig is the listener side. It requires the client certificate,
// pins it, and refuses every handshake once a connection exists.
func helperTLSConfig(cert tls.Certificate, clientFP string, accepted *atomic.Bool) *tls.Config {
	pinned := verifyPinned(clientFP)
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.RequireAnyClientCert,
		VerifyPeerCertificate: func(rawCerts [][]byte, chains [][]*x509.Certificate) error {
			if accepted.Load() {
				return errors.New("a connection already exists")
			}
			return pinned(rawCerts, chains)
		},
		NextProtos: []string{quicALPN},
		MinVersion: tls.VersionTLS13,
	}
}

// clientTLSConfig is the dial side. It presents the client certificate and
// pins the helper certificate.
func clientTLSConfig(cert tls.Certificate, helperFP string) *tls.Config {
	return &tls.Config{
		Certificates:          []tls.Certificate{cert},
		InsecureSkipVerify:    true,
		VerifyPeerCertificate: verifyPinned(helperFP),
		NextProtos:            []string{quicALPN},
		MinVersion:            tls.VersionTLS13,
	}
}
