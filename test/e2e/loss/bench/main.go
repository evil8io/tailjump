// Command bench is the throughput and latency tool of the loss job in
// test/e2e/loss.sh. The serve mode runs on the remote and streams zero bytes
// on request. The get mode downloads through a session and reports the rate.
// The connect mode times TCP connects through a session.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "serve":
		err = serve(os.Args[2:])
	case "get":
		err = get(os.Args[2:])
	case "connect":
		err = connect(os.Args[2:])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "bench:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: bench serve|get|connect [flags]")
	os.Exit(2)
}

// serve listens on each address, reads one line with a byte count per
// connection, and writes that many zero bytes. It deletes its own file at
// start and exits after the given seconds, so nothing remains on the remote.
func serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	listen := fs.String("listen", "", "comma-separated listen addresses, host:port")
	seconds := fs.Int("seconds", 1800, "exit after this many seconds")
	_ = fs.Parse(args)
	if *listen == "" {
		return fmt.Errorf("serve: -listen is required")
	}
	if len(os.Args) > 0 && os.Args[0] != "" {
		_ = os.Remove(os.Args[0])
	}
	time.AfterFunc(time.Duration(*seconds)*time.Second, func() { os.Exit(0) })

	zeros := make([]byte, 1<<20)
	for _, addr := range strings.Split(*listen, ",") {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return fmt.Errorf("serve: listen %s: %w", addr, err)
		}
		fmt.Println("listening on", ln.Addr())
		go func() {
			for {
				conn, err := ln.Accept()
				if err != nil {
					return
				}
				go handle(conn, zeros)
			}
		}()
	}
	select {}
}

func handle(conn net.Conn, zeros []byte) {
	defer func() { _ = conn.Close() }()
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return
	}
	want, err := strconv.ParseInt(strings.TrimSpace(line), 10, 64)
	if err != nil || want < 0 {
		return
	}
	_ = conn.SetReadDeadline(time.Time{})
	for want > 0 {
		n := int64(len(zeros))
		if want < n {
			n = want
		}
		w, err := conn.Write(zeros[:n])
		if err != nil {
			return
		}
		want -= int64(w)
	}
}

// get downloads mib mebibytes from the server and prints the rate. When the
// timeout passes first, it prints the rate of the bytes it received.
func get(args []string) error {
	fs := flag.NewFlagSet("get", flag.ExitOnError)
	addr := fs.String("addr", "", "server address, host:port")
	mib := fs.Int64("mib", 256, "mebibytes to request")
	timeout := fs.Duration("timeout", 60*time.Second, "time box for the download")
	_ = fs.Parse(args)
	if *addr == "" {
		return fmt.Errorf("get: -addr is required")
	}
	want := *mib << 20
	start := time.Now()
	conn, err := net.DialTimeout("tcp", *addr, 15*time.Second)
	if err != nil {
		return fmt.Errorf("get: dial %s: %w", *addr, err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(start.Add(*timeout))
	if _, err := fmt.Fprintf(conn, "%d\n", want); err != nil {
		return fmt.Errorf("get: request: %w", err)
	}
	got, err := io.Copy(io.Discard, io.LimitReader(conn, want))
	elapsed := time.Since(start).Seconds()
	complete := err == nil && got == want
	if !complete && err != nil && !os.IsTimeout(err) {
		fmt.Fprintln(os.Stderr, "get: read:", err)
	}
	mbit := float64(got) * 8 / 1e6 / elapsed
	fmt.Printf("bytes=%d seconds=%.2f mbit=%.1f complete=%t\n", got, elapsed, mbit, complete)
	return nil
}

// connect times n sequential TCP connects to addr and prints the p50, the
// p95, and the number of attempts that failed or hit the timeout.
func connect(args []string) error {
	fs := flag.NewFlagSet("connect", flag.ExitOnError)
	addr := fs.String("addr", "", "address to connect to, host:port")
	n := fs.Int("n", 20, "number of attempts")
	timeout := fs.Duration("timeout", 8*time.Second, "timeout per attempt")
	_ = fs.Parse(args)
	if *addr == "" {
		return fmt.Errorf("connect: -addr is required")
	}
	var ms []float64
	failed := 0
	for i := 0; i < *n; i++ {
		start := time.Now()
		conn, err := net.DialTimeout("tcp", *addr, *timeout)
		elapsed := float64(time.Since(start).Microseconds()) / 1000
		if err != nil {
			failed++
			fmt.Fprintf(os.Stderr, "connect: attempt %d: %v\n", i+1, err)
			continue
		}
		_ = conn.Close()
		ms = append(ms, elapsed)
		time.Sleep(100 * time.Millisecond)
	}
	sort.Float64s(ms)
	fmt.Printf("attempts=%d ok=%d failed=%d p50_ms=%.1f p95_ms=%.1f max_ms=%.1f\n",
		*n, len(ms), failed, percentile(ms, 50), percentile(ms, 95), percentile(ms, 100))
	return nil
}

// percentile returns the nearest-rank percentile of sorted values, and
// zero for no values.
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
