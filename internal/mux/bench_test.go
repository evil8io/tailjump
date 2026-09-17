package mux

import (
	"context"
	"encoding/binary"
	"errors"
	"runtime"
	"testing"
	"time"
)

// benchClient is the Bench method the yamux Client and the QUICClient share.
type benchClient interface {
	Bench(ctx context.Context, dir BenchDirection, d time.Duration) (BenchResult, error)
}

// benchDirections are the two runs every transport test makes.
var benchDirections = []struct {
	name string
	dir  BenchDirection
}{
	{"up", BenchUp},
	{"down", BenchDown},
}

// checkBench runs one direction for a second and checks that it moved bytes
// and that the clock covered the whole run.
func checkBench(t *testing.T, client benchClient, dir BenchDirection) {
	t.Helper()
	res, err := client.Bench(context.Background(), dir, time.Second)
	if err != nil {
		t.Fatalf("Bench: %v", err)
	}
	if res.Bytes == 0 {
		t.Fatal("the run moved no bytes")
	}
	if res.Elapsed < time.Second {
		t.Fatalf("elapsed = %s, want at least 1s", res.Elapsed)
	}
	if res.Rate() == 0 {
		t.Fatalf("rate = 0 for %d bytes in %s", res.Bytes, res.Elapsed)
	}
}

func TestLoopbackBench(t *testing.T) {
	client := newLoopback(t, &Server{})
	for _, c := range benchDirections {
		t.Run(c.name, func(t *testing.T) { checkBench(t, client, c.dir) })
	}
}

func TestQUICLoopbackBench(t *testing.T) {
	_, q, _, _ := newQUICLoopback(t, &Server{})
	for _, c := range benchDirections {
		t.Run(c.name, func(t *testing.T) { checkBench(t, q, c.dir) })
	}
}

// TestBenchInvalidDuration locks in that the client refuses a duration the
// helper would refuse, before it sends anything.
func TestBenchInvalidDuration(t *testing.T) {
	client := newLoopback(t, &Server{})
	for _, d := range []time.Duration{0, 500 * time.Millisecond, 1500 * time.Millisecond, 31 * time.Second} {
		if _, err := client.Bench(context.Background(), BenchUp, d); err == nil {
			t.Fatalf("Bench with duration %s returned no error", d)
		}
	}
}

// TestBenchInvalidDirection locks in that the helper refuses a direction
// outside the protocol with the other status.
func TestBenchInvalidDirection(t *testing.T) {
	client := newLoopback(t, &Server{})
	_, err := client.Bench(context.Background(), BenchDirection(2), time.Second)
	if !errors.Is(err, ErrDialFailed) {
		t.Fatalf("Bench with direction 2 = %v, want the other status", err)
	}
}

// benchStatusFor sends a raw bench request on a new stream and returns the
// helper's status byte.
func benchStatusFor(t *testing.T, client *Client, dir byte, secs uint16) byte {
	t.Helper()
	stream, err := client.sess.OpenStream()
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	defer func() { _ = stream.Close() }()

	req := binary.BigEndian.AppendUint16([]byte{kindBench, dir}, secs)
	if _, err := stream.Write(req); err != nil {
		t.Fatalf("write the request: %v", err)
	}
	_ = stream.SetReadDeadline(time.Now().Add(5 * time.Second))
	status, err := readStatus(stream)
	if err != nil {
		t.Fatalf("read the status: %v", err)
	}
	return status
}

// TestBenchRequestValidation locks in the helper's own check of the request,
// which a client with another version can reach.
func TestBenchRequestValidation(t *testing.T) {
	client := newLoopback(t, &Server{})
	cases := []struct {
		name string
		dir  byte
		secs uint16
		want byte
	}{
		{"up for one second", 0, 1, statusOK},
		{"down for one second", 1, 1, statusOK},
		{"unknown direction", 2, 5, statusOther},
		{"zero seconds", 0, 0, statusOther},
		{"above the cap", 0, benchMaxSeconds + 1, statusOther},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := benchStatusFor(t, client, c.dir, c.secs); got != c.want {
				t.Fatalf("status = %d, want %d", got, c.want)
			}
		})
	}
}

// TestBenchContextCancel locks in that a cancelled context ends the run at
// once and leaves no goroutine behind.
func TestBenchContextCancel(t *testing.T) {
	client := newLoopback(t, &Server{})
	before := runtime.NumGoroutine()

	ctx, cancel := context.WithCancel(context.Background())
	timer := time.AfterFunc(100*time.Millisecond, cancel)
	defer timer.Stop()

	start := time.Now()
	_, err := client.Bench(ctx, BenchUp, 30*time.Second)
	elapsed := time.Since(start)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Bench with a cancelled context = %v, want context.Canceled", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("Bench returned after %s, want a prompt return", elapsed)
	}

	deadline := time.Now().Add(3 * time.Second)
	for runtime.NumGoroutine() > before {
		if time.Now().After(deadline) {
			t.Fatalf("goroutines = %d after the cancel, want at most %d", runtime.NumGoroutine(), before)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
