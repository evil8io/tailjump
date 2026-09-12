// Command spike-helper-quic is the helper-size variant of the K8S-208 spike on
// upstream quic-go. It is cmd/tjhelper plus a QUIC listener that accepts one
// connection and serves one stream, so the linker keeps the QUIC and TLS code.
package main

import (
	"context"
	"net"
	"os"
	"time"

	"github.com/quic-go/quic-go"

	"github.com/evil8io/tailjump/internal/helper"
	"github.com/evil8io/tailjump/internal/spikenet"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "quic" {
		if err := serve(os.Args[2]); err != nil {
			os.Exit(1)
		}
		return
	}
	helper.Main()
}

func serve(addr string) error {
	ua, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return err
	}
	pc, err := net.ListenUDP("udp", ua)
	if err != nil {
		return err
	}
	_ = pc.SetReadBuffer(2 << 20)
	tlsConf, err := spikenet.ServerTLS()
	if err != nil {
		return err
	}
	ln, err := quic.Listen(pc, tlsConf, &quic.Config{
		MaxIdleTimeout:          30 * time.Second,
		KeepAlivePeriod:         10 * time.Second,
		InitialPacketSize:       1232,
		DisablePathMTUDiscovery: true,
		MaxStreamReceiveWindow:  8 << 20,
	})
	if err != nil {
		return err
	}
	conn, err := ln.Accept(context.Background())
	if err != nil {
		return err
	}
	st, err := conn.AcceptStream(context.Background())
	if err != nil {
		return err
	}
	cmd, n, err := spikenet.ReadRequest(st)
	if err != nil {
		return err
	}
	if cmd == spikenet.CmdDownload {
		_ = spikenet.WriteZeros(st, n)
	}
	return st.Close()
}
