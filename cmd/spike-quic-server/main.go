// Command spike-quic-server is the remote side of the K8S-208 QUIC spike. It
// serves a QUIC listener, a raw TCP listener, and a raw UDP probe responder on
// the tailnet address, and it exits after a deadline. It deletes its own file
// at start, as the real helper does, so no file remains on a remote.
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/quic-go/quic-go"

	"github.com/evil8io/tailjump/internal/spikenet"
)

func main() {
	quicAddr := flag.String("quic", "", "QUIC listen address with the library defaults")
	quicTuned := flag.String("quic-tuned", "", "QUIC listen address with the K8S-206 settings")
	tcpAddr := flag.String("tcp", "", "raw TCP listen address")
	udpAddr := flag.String("udp", "", "raw UDP probe listen address")
	ttl := flag.Duration("ttl", 10*time.Minute, "exit after this time")
	tuned := flag.Bool("tuned", false, "apply the K8S-206 settings")
	ipsize := flag.Int("ipsize", 0, "override the QUIC initial packet size")
	keep := flag.Bool("keep", false, "do not delete the binary at start")
	flag.Parse()

	if !*keep {
		spikenet.SelfDelete()
	}

	if *tcpAddr != "" {
		ln, err := net.Listen("tcp", *tcpAddr)
		if err != nil {
			die(err)
		}
		go spikenet.ServeTCP(ln)
	}
	if *udpAddr != "" {
		ua, err := net.ResolveUDPAddr("udp", *udpAddr)
		if err != nil {
			die(err)
		}
		c, err := net.ListenUDP("udp", ua)
		if err != nil {
			die(err)
		}
		go spikenet.ServeUDPProbe(c)
	}
	if *quicAddr != "" {
		if err := serveQUIC(*quicAddr, *tuned, *ipsize); err != nil {
			die(err)
		}
	}
	if *quicTuned != "" {
		if err := serveQUIC(*quicTuned, true, *ipsize); err != nil {
			die(err)
		}
	}

	fmt.Printf("spike-quic-server up quic=%s quic-tuned=%s tcp=%s udp=%s tuned=%v ttl=%s\n",
		*quicAddr, *quicTuned, *tcpAddr, *udpAddr, *tuned, *ttl)
	time.Sleep(*ttl)
}

func serveQUIC(addr string, tuned bool, ipsize int) error {
	ua, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return err
	}
	pc, err := net.ListenUDP("udp", ua)
	if err != nil {
		return err
	}
	if tuned {
		_ = pc.SetReadBuffer(2 << 20)
		_ = pc.SetWriteBuffer(2 << 20)
	}
	tlsConf, err := spikenet.ServerTLS()
	if err != nil {
		return err
	}
	ln, err := quic.Listen(pc, tlsConf, config(tuned, ipsize))
	if err != nil {
		return err
	}
	go accept(ln)
	return nil
}

func config(tuned bool, ipsize int) *quic.Config {
	c := &quic.Config{
		MaxIdleTimeout:  30 * time.Second,
		KeepAlivePeriod: 10 * time.Second,
	}
	if tuned {
		c.InitialPacketSize = 1232
		c.DisablePathMTUDiscovery = true
		c.InitialStreamReceiveWindow = 8 << 20
		c.MaxStreamReceiveWindow = 8 << 20
		c.InitialConnectionReceiveWindow = 20 << 20
		c.MaxConnectionReceiveWindow = 20 << 20
	}
	if ipsize > 0 {
		c.InitialPacketSize = uint16(ipsize)
	}
	return c
}

func accept(ln *quic.Listener) {
	for {
		conn, err := ln.Accept(context.Background())
		if err != nil {
			return
		}
		go serveConn(conn)
	}
}

func serveConn(conn *quic.Conn) {
	for {
		st, err := conn.AcceptStream(context.Background())
		if err != nil {
			return
		}
		go func(st *quic.Stream) {
			cmd, n, err := spikenet.ReadRequest(st)
			if err != nil || cmd != spikenet.CmdDownload {
				st.Close()
				return
			}
			_ = spikenet.WriteZeros(st, n)
			st.Close()
		}(st)
	}
}

func die(err error) {
	fmt.Fprintln(os.Stderr, "spike-quic-server:", err)
	os.Exit(1)
}
