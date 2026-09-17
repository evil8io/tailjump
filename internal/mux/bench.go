package mux

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"time"
)

const (
	// benchBlock is the size of one block both sides send.
	benchBlock = 32 << 10
	// benchMaxSeconds is the longest run the helper accepts. It bounds the
	// load a client can put on a remote.
	benchMaxSeconds = 30
	// benchRequestLen is the request after the kind byte: 1 byte direction
	// and 2 bytes seconds.
	benchRequestLen = 3
	// benchCountLen is the byte count the helper returns after an up run.
	benchCountLen = 8

	// benchHelperGrace is the extra read time the helper gives an up run,
	// on top of the duration.
	benchHelperGrace = 10 * time.Second
	// benchCountTimeout is how long the client waits for the helper's byte
	// count after it half-closed the stream.
	benchCountTimeout = 30 * time.Second
	// benchDownGrace is the extra read time the client gives a down run, on
	// top of the duration.
	benchDownGrace = 30 * time.Second
)

// BenchDirection is the direction of one bench run. The values are the
// direction byte of the request.
type BenchDirection byte

// The bench directions.
const (
	// BenchUp sends from the client to the helper.
	BenchUp BenchDirection = 0
	// BenchDown sends from the helper to the client.
	BenchDown BenchDirection = 1
)

// BenchResult is one measured direction: the bytes the receiver counted and
// the time the run took.
type BenchResult struct {
	Bytes   uint64
	Elapsed time.Duration
}

// Rate returns the throughput in bytes per second, and zero when the run
// took no time.
func (r BenchResult) Rate() uint64 {
	if r.Elapsed <= 0 {
		return 0
	}
	return uint64(float64(r.Bytes) / r.Elapsed.Seconds())
}

// Bench measures the throughput to the helper in one direction over a new
// stream of the yamux session.
func (c *Client) Bench(ctx context.Context, dir BenchDirection, d time.Duration) (BenchResult, error) {
	stream, err := c.sess.OpenStream()
	if err != nil {
		return BenchResult{}, err
	}
	return runBench(ctx, stream, dir, d)
}

// Bench measures the throughput to the helper in one direction over a new
// stream of the QUIC connection.
func (q *QUICClient) Bench(ctx context.Context, dir BenchDirection, d time.Duration) (BenchResult, error) {
	openCtx, cancel := context.WithTimeout(ctx, quicOpenTimeout)
	defer cancel()
	st, err := q.conn.OpenStreamSync(openCtx)
	if err != nil {
		return BenchResult{}, fmt.Errorf("quic: open stream: %w", err)
	}
	return runBench(ctx, wrapStream(st, q.conn), dir, d)
}

// runBench measures one direction on an open stream. A cancelled context
// ends a blocked read or write and the call then returns the context error.
func runBench(ctx context.Context, stream Stream, dir BenchDirection, d time.Duration) (BenchResult, error) {
	stop := watchBench(ctx, stream)
	defer releaseStream(stream)
	defer stop()

	res, err := benchStream(ctx, stream, dir, d)
	if err != nil {
		if ctx.Err() != nil {
			return BenchResult{}, ctx.Err()
		}
		return BenchResult{}, err
	}
	return res, nil
}

// benchStream sends the request, waits for the helper's status, and runs the
// direction the request named.
func benchStream(ctx context.Context, stream Stream, dir BenchDirection, d time.Duration) (BenchResult, error) {
	if err := writeBenchRequest(stream, dir, d); err != nil {
		return BenchResult{}, err
	}
	status, err := readStatus(stream)
	if err != nil {
		return BenchResult{}, err
	}
	if status != statusOK {
		return BenchResult{}, statusError(status)
	}
	if dir == BenchDown {
		return benchDown(ctx, stream, d)
	}
	return benchUp(ctx, stream, d)
}

// benchUp sends blocks for the duration, half-closes the stream, and reads
// the number of bytes the helper received. That count, not the bytes the
// client wrote, is the result, because the transport buffers several MiB.
// The clock runs until the count arrives, so the drain of those buffers is
// part of the measurement.
func benchUp(ctx context.Context, stream Stream, d time.Duration) (BenchResult, error) {
	block := make([]byte, benchBlock)
	start := time.Now()
	for end := start.Add(d); time.Now().Before(end); {
		if ctx.Err() != nil {
			return BenchResult{}, ctx.Err()
		}
		if _, err := stream.Write(block); err != nil {
			return BenchResult{}, err
		}
	}
	if err := stream.Close(); err != nil {
		return BenchResult{}, err
	}
	_ = stream.SetReadDeadline(time.Now().Add(benchCountTimeout))
	var buf [benchCountLen]byte
	if _, err := io.ReadFull(stream, buf[:]); err != nil {
		return BenchResult{}, fmt.Errorf("bench count: %w", err)
	}
	return BenchResult{Bytes: binary.BigEndian.Uint64(buf[:]), Elapsed: time.Since(start)}, nil
}

// benchDown counts what the helper sends until the end of the stream.
func benchDown(ctx context.Context, stream Stream, d time.Duration) (BenchResult, error) {
	_ = stream.SetReadDeadline(time.Now().Add(d + benchDownGrace))
	buf := make([]byte, benchBlock)
	start := time.Now()
	var total uint64
	for {
		n, err := stream.Read(buf)
		total += uint64(n)
		if errors.Is(err, io.EOF) {
			return BenchResult{Bytes: total, Elapsed: time.Since(start)}, nil
		}
		if err != nil {
			return BenchResult{}, err
		}
		if ctx.Err() != nil {
			return BenchResult{}, ctx.Err()
		}
	}
}

// watchBench expires the stream deadlines when ctx ends, so a blocked read
// or write returns at once. The returned function ends the goroutine and
// waits for it, so no goroutine outlives the call.
func watchBench(ctx context.Context, stream Stream) func() {
	done := make(chan struct{})
	ended := make(chan struct{})
	go func() {
		defer close(ended)
		select {
		case <-ctx.Done():
			now := time.Now()
			_ = stream.SetReadDeadline(now)
			if w, ok := stream.(writeDeadliner); ok {
				_ = w.SetWriteDeadline(now)
			}
		case <-done:
		}
	}()
	return func() {
		close(done)
		<-ended
	}
}

// writeDeadliner is a Stream with a write deadline. The yamux stream and the
// QUIC stream both have one.
type writeDeadliner interface {
	SetWriteDeadline(t time.Time) error
}

// releaseStream ends both directions. The first Close is a half-close on
// yamux and on the QUIC wrapper, and the second releases the read side.
func releaseStream(stream Stream) {
	_ = stream.Close()
	_ = stream.Close()
}

// writeBenchRequest sends the kind byte, the direction, and the duration in
// whole seconds.
func writeBenchRequest(w io.Writer, dir BenchDirection, d time.Duration) error {
	secs := d / time.Second
	if secs < 1 || secs > benchMaxSeconds || d%time.Second != 0 {
		return fmt.Errorf("mux: bench duration %s: want 1s to %ds in whole seconds", d, benchMaxSeconds)
	}
	buf := []byte{kindBench, byte(dir)}
	buf = binary.BigEndian.AppendUint16(buf, uint16(secs))
	_, err := w.Write(buf)
	return err
}

// readBenchRequest reads the direction and the duration of a bench stream
// and refuses a value outside the protocol.
func readBenchRequest(r io.Reader) (BenchDirection, time.Duration, error) {
	var buf [benchRequestLen]byte
	if _, err := io.ReadFull(r, buf[:]); err != nil {
		return 0, 0, err
	}
	dir := BenchDirection(buf[0])
	if dir != BenchUp && dir != BenchDown {
		return 0, 0, fmt.Errorf("mux: bench direction %d", buf[0])
	}
	secs := binary.BigEndian.Uint16(buf[1:])
	if secs < 1 || secs > benchMaxSeconds {
		return 0, 0, fmt.Errorf("mux: bench duration %d seconds", secs)
	}
	return dir, time.Duration(secs) * time.Second, nil
}

// handleBench serves a bench stream. An invalid request gets the other
// status and a close.
func (s *Server) handleBench(stream Stream) {
	defer releaseStream(stream)
	dir, d, err := readBenchRequest(stream)
	if err != nil {
		s.logf("bench request: %v", err)
		_, _ = stream.Write([]byte{statusOther})
		return
	}
	if _, err := stream.Write([]byte{statusOK}); err != nil {
		return
	}
	if dir == BenchDown {
		serveBenchDown(stream, d)
		return
	}
	serveBenchUp(stream, d)
}

// serveBenchUp discards what the client sends and answers with the number of
// bytes it received. A read error or the deadline ends the run without a
// count.
func serveBenchUp(stream Stream, d time.Duration) {
	_ = stream.SetReadDeadline(time.Now().Add(d + benchHelperGrace))
	n, err := io.Copy(io.Discard, stream)
	if err != nil {
		return
	}
	var buf [benchCountLen]byte
	binary.BigEndian.PutUint64(buf[:], uint64(n))
	_, _ = stream.Write(buf[:])
}

// serveBenchDown sends blocks until the duration has passed. A write error
// ends the run.
func serveBenchDown(stream Stream, d time.Duration) {
	block := make([]byte, benchBlock)
	for end := time.Now().Add(d); time.Now().Before(end); {
		if _, err := stream.Write(block); err != nil {
			return
		}
	}
}
