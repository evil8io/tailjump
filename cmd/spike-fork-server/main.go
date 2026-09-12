// Command spike-fork-server is the remote side of the K8S-208 QUIC spike on
// the apernet/quic-go fork. It serves the same download command as
// spike-quic-server and installs the congestion controller the flag names.
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"time"

	quic "github.com/apernet/quic-go"

	"github.com/evil8io/tailjump/internal/spikecc/setcc"
	"github.com/evil8io/tailjump/internal/spikenet"
)

func main() {
	quicAddr := flag.String("quic", "", "QUIC listen address")
	ttl := flag.Duration("ttl", 10*time.Minute, "exit after this time")
	tuned := flag.Bool("tuned", false, "apply the K8S-206 settings")
	cc := flag.String("cc", "cubic", "congestion controller: cubic, bbr, or brutal")
	quicAddr2 := flag.String("quic2", "", "second QUIC listen address")
	cc2 := flag.String("cc2", "bbr", "congestion controller of the second listener")
	brutalMbps := flag.Uint64("brutal-mbps", 0, "brutal rate in Mbit/s")
	keep := flag.Bool("keep", false, "do not delete the binary at start")
	flag.Parse()

	if !*keep {
		spikenet.SelfDelete()
	}
	if err := serve(*quicAddr, *tuned, *cc, *brutalMbps*1e6/8); err != nil {
		fmt.Fprintln(os.Stderr, "spike-fork-server:", err)
		os.Exit(1)
	}
	if *quicAddr2 != "" {
		if err := serve(*quicAddr2, *tuned, *cc2, *brutalMbps*1e6/8); err != nil {
			fmt.Fprintln(os.Stderr, "spike-fork-server:", err)
			os.Exit(1)
		}
	}
	fmt.Printf("spike-fork-server up quic=%s cc=%s quic2=%s cc2=%s tuned=%v ttl=%s\n",
		*quicAddr, *cc, *quicAddr2, *cc2, *tuned, *ttl)
	time.Sleep(*ttl)
}

func serve(addr string, tuned bool, cc string, brutalBps uint64) error {
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
	c := &quic.Config{MaxIdleTimeout: 30 * time.Second, KeepAlivePeriod: 10 * time.Second}
	if tuned {
		c.InitialPacketSize = 1232
		c.DisablePathMTUDiscovery = true
		c.InitialStreamReceiveWindow = 8 << 20
		c.MaxStreamReceiveWindow = 8 << 20
		c.InitialConnectionReceiveWindow = 20 << 20
		c.MaxConnectionReceiveWindow = 20 << 20
	}
	ln, err := quic.Listen(pc, tlsConf, c)
	if err != nil {
		return err
	}
	go func() {
		for {
			conn, err := ln.Accept(context.Background())
			if err != nil {
				return
			}
			if err := setcc.Apply(conn, cc, brutalBps); err != nil {
				fmt.Fprintln(os.Stderr, "spike-fork-server: cc:", err)
			}
			go serveConn(conn)
		}
	}()
	return nil
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
