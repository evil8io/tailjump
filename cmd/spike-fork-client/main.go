// Command spike-fork-client is the client side of the K8S-208 QUIC spike on
// the apernet/quic-go fork. It measures the handshake time and the download
// rate with the congestion controller the flag names.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	quic "github.com/apernet/quic-go"

	"github.com/evil8io/tailjump/internal/spikecc/setcc"
	"github.com/evil8io/tailjump/internal/spikenet"
)

func main() {
	mode := flag.String("mode", "download", "handshake or download")
	addr := flag.String("addr", "", "server address host:port")
	runs := flag.Int("runs", 3, "number of runs")
	n := flag.Uint64("n", 64<<20, "download size in bytes")
	tuned := flag.Bool("tuned", false, "apply the K8S-206 settings")
	cc := flag.String("cc", "cubic", "congestion controller: cubic, bbr, or brutal")
	brutalMbps := flag.Uint64("brutal-mbps", 0, "brutal rate in Mbit/s")
	pause := flag.Duration("pause", 2*time.Second, "pause between runs")
	flag.Parse()

	if *addr == "" {
		fail(fmt.Errorf("set -addr"))
	}
	var err error
	switch *mode {
	case "handshake":
		err = handshake(*addr, *runs, *tuned, *cc, *brutalMbps*1e6/8, *pause)
	case "download":
		err = download(*addr, *runs, *n, *tuned, *cc, *brutalMbps*1e6/8, *pause)
	default:
		err = fmt.Errorf("unknown mode %q", *mode)
	}
	if err != nil {
		fail(err)
	}
}

func config(tuned bool) *quic.Config {
	c := &quic.Config{MaxIdleTimeout: 30 * time.Second, KeepAlivePeriod: 10 * time.Second}
	if tuned {
		c.InitialPacketSize = 1232
		c.DisablePathMTUDiscovery = true
		c.InitialStreamReceiveWindow = 8 << 20
		c.MaxStreamReceiveWindow = 8 << 20
		c.InitialConnectionReceiveWindow = 20 << 20
		c.MaxConnectionReceiveWindow = 20 << 20
	}
	return c
}

func dial(addr string, tuned bool, cc string, brutalBps uint64) (*quic.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	conn, err := quic.DialAddr(ctx, addr, spikenet.ClientTLS(), config(tuned))
	if err != nil {
		return nil, err
	}
	if err := setcc.Apply(conn, cc, brutalBps); err != nil {
		return nil, err
	}
	return conn, nil
}

func handshake(addr string, runs int, tuned bool, cc string, brutalBps uint64, pause time.Duration) error {
	var ms []float64
	for i := 0; i < runs; i++ {
		start := time.Now()
		conn, err := dial(addr, tuned, cc, brutalBps)
		d := time.Since(start)
		if err != nil {
			return err
		}
		_ = conn.CloseWithError(0, "")
		ms = append(ms, float64(d.Microseconds())/1000)
		fmt.Printf("handshake run %d: %.1f ms\n", i+1, ms[i])
		time.Sleep(pause)
	}
	fmt.Printf("handshake median: %.1f ms over %d runs\n", spikenet.Median(ms), runs)
	return nil
}

func download(addr string, runs int, n uint64, tuned bool, cc string, brutalBps uint64, pause time.Duration) error {
	var rates []float64
	for i := 0; i < runs; i++ {
		conn, err := dial(addr, tuned, cc, brutalBps)
		if err != nil {
			return err
		}
		st, err := conn.OpenStreamSync(context.Background())
		if err != nil {
			return err
		}
		start := time.Now()
		if err := spikenet.WriteRequest(st, spikenet.CmdDownload, n); err != nil {
			return err
		}
		got, err := spikenet.Drain(st)
		d := time.Since(start)
		_ = conn.CloseWithError(0, "")
		if err != nil {
			return fmt.Errorf("after %d bytes: %w", got, err)
		}
		r := spikenet.Mbits(got, d)
		rates = append(rates, r)
		fmt.Printf("download run %d cc=%s: %d bytes in %.2f s, %.1f Mbit/s\n", i+1, cc, got, d.Seconds(), r)
		time.Sleep(pause)
	}
	fmt.Printf("download median cc=%s: %.1f Mbit/s over %d runs\n", cc, spikenet.Median(rates), runs)
	return nil
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "spike-fork-client:", err)
	os.Exit(1)
}
