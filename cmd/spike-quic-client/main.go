// Command spike-quic-client is the client side of the K8S-208 QUIC spike. It
// measures the handshake time, the download rate over one QUIC stream, the
// packet size QUIC settles on, the raw TCP rate, and which raw UDP payload
// sizes cross the path.
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/qlog"

	"github.com/evil8io/tailjump/internal/spikenet"
)

func main() {
	mode := flag.String("mode", "download", "handshake, download, mtu, tcp, or udpprobe")
	addr := flag.String("addr", "", "server address host:port")
	runs := flag.Int("runs", 3, "number of runs")
	n := flag.Uint64("n", 256<<20, "download size in bytes")
	tuned := flag.Bool("tuned", false, "apply the K8S-206 settings")
	pmtud := flag.Bool("pmtud", false, "leave path MTU discovery on, overriding -tuned")
	sizes := flag.String("sizes", "1200,1232,1252,1280,1400", "raw UDP probe payload sizes")
	ipsize := flag.Int("ipsize", 0, "override the QUIC initial packet size")
	df := flag.Bool("df", false, "set the do-not-fragment bit on the raw UDP probe")
	qlogOn := flag.Bool("qlog", false, "write a qlog trace to $QLOGDIR")
	pause := flag.Duration("pause", 2*time.Second, "pause between runs")
	flag.Parse()
	useQlog = *qlogOn

	if *addr == "" {
		die(fmt.Errorf("set -addr"))
	}
	var err error
	switch *mode {
	case "handshake":
		err = handshake(*addr, *runs, *tuned, *pmtud, *ipsize, *pause)
	case "download":
		err = download(*addr, *runs, *n, *tuned, *pmtud, *ipsize, *pause)
	case "mtu":
		err = mtu(*addr, *tuned, *pmtud, *ipsize)
	case "tcp":
		err = tcp(*addr, *runs, *n, *pause)
	case "udpprobe":
		err = udpprobe(*addr, *sizes, *runs, *df)
	default:
		err = fmt.Errorf("unknown mode %q", *mode)
	}
	if err != nil {
		die(err)
	}
}

func config(tuned, pmtud bool, ipsize int) *quic.Config {
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
	if pmtud {
		c.DisablePathMTUDiscovery = false
	}
	if ipsize > 0 {
		c.InitialPacketSize = uint16(ipsize)
	}
	if useQlog {
		c.Tracer = qlog.DefaultConnectionTracer
	}
	return c
}

// useQlog turns on the qlog trace, which records the mtu_updated events that
// show what path MTU discovery settles on.
var useQlog bool

func dial(ctx context.Context, addr string, tuned, pmtud bool, ipsize int, pc net.PacketConn) (*quic.Conn, error) {
	if pc == nil {
		return quic.DialAddr(ctx, addr, spikenet.ClientTLS(), config(tuned, pmtud, ipsize))
	}
	ua, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, err
	}
	return quic.Dial(ctx, pc, ua, spikenet.ClientTLS(), config(tuned, pmtud, ipsize))
}

func handshake(addr string, runs int, tuned, pmtud bool, ipsize int, pause time.Duration) error {
	var ms []float64
	for i := 0; i < runs; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		start := time.Now()
		conn, err := dial(ctx, addr, tuned, pmtud, ipsize, nil)
		d := time.Since(start)
		cancel()
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

func download(addr string, runs int, n uint64, tuned, pmtud bool, ipsize int, pause time.Duration) error {
	var rates []float64
	for i := 0; i < runs; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		conn, err := dial(ctx, addr, tuned, pmtud, ipsize, nil)
		cancel()
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
		fmt.Printf("download run %d: %d bytes in %.2f s, %.1f Mbit/s\n", i+1, got, d.Seconds(), r)
		time.Sleep(pause)
	}
	fmt.Printf("download median: %.1f Mbit/s over %d runs\n", spikenet.Median(rates), runs)
	return nil
}

func mtu(addr string, tuned, pmtud bool, ipsize int) error {
	pc, err := net.ListenUDP("udp", &net.UDPAddr{})
	if err != nil {
		return err
	}
	sc := &spikenet.SizeConn{PacketConn: pc}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	conn, err := dial(ctx, addr, tuned, pmtud, ipsize, sc)
	cancel()
	if err != nil {
		return err
	}
	st, err := conn.OpenStreamSync(context.Background())
	if err != nil {
		return err
	}
	if err := spikenet.WriteRequest(st, spikenet.CmdDownload, 16<<20); err != nil {
		return err
	}
	got, err := spikenet.Drain(st)
	_ = conn.CloseWithError(0, "")
	if err != nil {
		return err
	}
	fmt.Printf("mtu tuned=%v pmtud=%v: %d bytes, largest datagram sent %d, largest received %d\n",
		tuned, pmtud, got, sc.MaxWrite.Load(), sc.MaxRead.Load())
	return nil
}

func tcp(addr string, runs int, n uint64, pause time.Duration) error {
	var rates []float64
	for i := 0; i < runs; i++ {
		c, err := net.DialTimeout("tcp", addr, 15*time.Second)
		if err != nil {
			return err
		}
		start := time.Now()
		if err := spikenet.WriteRequest(c, spikenet.CmdDownload, n); err != nil {
			return err
		}
		got, err := spikenet.Drain(c)
		d := time.Since(start)
		c.Close()
		if err != nil {
			return err
		}
		r := spikenet.Mbits(got, d)
		rates = append(rates, r)
		fmt.Printf("tcp run %d: %d bytes in %.2f s, %.1f Mbit/s\n", i+1, got, d.Seconds(), r)
		time.Sleep(pause)
	}
	fmt.Printf("tcp median: %.1f Mbit/s over %d runs\n", spikenet.Median(rates), runs)
	return nil
}

func udpprobe(addr, sizes string, runs int, df bool) error {
	ua, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return err
	}
	c, err := net.DialUDP("udp", nil, ua)
	if err != nil {
		return err
	}
	defer c.Close()
	if df {
		if err := setDF(c); err != nil {
			return err
		}
	}
	buf := make([]byte, 2048)
	for _, s := range strings.Split(sizes, ",") {
		size, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil {
			return err
		}
		ok := 0
		reply := ""
		for i := 0; i < runs; i++ {
			p := make([]byte, size)
			if _, err := c.Write(p); err != nil {
				reply = "send error: " + err.Error()
				break
			}
			_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
			n, err := c.Read(buf)
			if err != nil {
				continue
			}
			ok++
			reply = string(buf[:n])
		}
		fmt.Printf("udp payload %d: %d/%d answered, reply %q\n", size, ok, runs, reply)
	}
	return nil
}

// setDF sets the do-not-fragment bit, so a probe larger than the path MTU is
// dropped instead of fragmented. QUIC sets the same bit.
func setDF(c *net.UDPConn) error {
	raw, err := c.SyscallConn()
	if err != nil {
		return err
	}
	var serr error
	err = raw.Control(func(fd uintptr) {
		serr = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IP, syscall.IP_MTU_DISCOVER, 2)
	})
	if err != nil {
		return err
	}
	return serr
}

func die(err error) {
	fmt.Fprintln(os.Stderr, "spike-quic-client:", err)
	os.Exit(1)
}
