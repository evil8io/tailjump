package mux

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/netip"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestDestRoundTrip(t *testing.T) {
	cases := []netip.AddrPort{
		netip.MustParseAddrPort("10.0.0.2:53"),
		netip.MustParseAddrPort("192.0.2.1:65535"),
		netip.MustParseAddrPort("[2001:db8::1]:443"),
		netip.MustParseAddrPort("[::1]:1"),
	}
	for _, want := range cases {
		var buf bytes.Buffer
		if err := writeDest(&buf, want); err != nil {
			t.Fatalf("writeDest(%v): %v", want, err)
		}
		got, err := readDest(&buf)
		if err != nil {
			t.Fatalf("readDest(%v): %v", want, err)
		}
		if got != want {
			t.Fatalf("round trip = %v, want %v", got, want)
		}
	}
}

func TestWriteDestUnmapsV4(t *testing.T) {
	var buf bytes.Buffer
	if err := writeDest(&buf, netip.MustParseAddrPort("[::ffff:10.0.0.2]:53")); err != nil {
		t.Fatal(err)
	}
	if got := buf.Bytes()[0]; got != 4 {
		t.Fatalf("address length = %d, want 4 for a v4-mapped address", got)
	}
}

func TestReadDestInvalidLength(t *testing.T) {
	_, err := readDest(bytes.NewReader([]byte{7, 1, 2, 3, 4, 5, 6, 7}))
	if err == nil {
		t.Fatal("readDest with length 7 returned no error")
	}
}

func TestFrameRoundTrip(t *testing.T) {
	for _, payload := range [][]byte{[]byte("hello"), {}, bytes.Repeat([]byte{0x41}, maxUDPFrame)} {
		var buf bytes.Buffer
		if err := writeFrame(&buf, payload); err != nil {
			t.Fatalf("writeFrame(len=%d): %v", len(payload), err)
		}
		got, err := readFrame(&buf)
		if err != nil {
			t.Fatalf("readFrame(len=%d): %v", len(payload), err)
		}
		if !bytes.Equal(got, payload) {
			t.Fatalf("round trip len = %d, want %d", len(got), len(payload))
		}
	}
}

func TestFrameTooLarge(t *testing.T) {
	if err := writeFrame(io.Discard, make([]byte, maxUDPFrame+1)); err == nil {
		t.Fatal("writeFrame over the maximum returned no error")
	}
}

func TestIdleFor(t *testing.T) {
	def := 60 * time.Second
	dns := 10 * time.Second
	if got := idleFor(53, def, dns); got != dns {
		t.Fatalf("idleFor(53) = %s, want %s", got, dns)
	}
	if got := idleFor(443, def, dns); got != def {
		t.Fatalf("idleFor(443) = %s, want %s", got, def)
	}
}

func TestStatusError(t *testing.T) {
	cases := map[byte]error{
		statusRefused:     ErrRefused,
		statusUnreachable: ErrUnreachable,
		statusTimeout:     ErrDialTimeout,
		statusOther:       ErrDialFailed,
	}
	for status, want := range cases {
		if got := statusError(status); !errors.Is(got, want) {
			t.Fatalf("statusError(%d) = %v, want %v", status, got, want)
		}
	}
}

type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

func TestStatusFor(t *testing.T) {
	cases := []struct {
		err  error
		want byte
	}{
		{timeoutError{}, statusTimeout},
		{context.DeadlineExceeded, statusTimeout},
		{syscall.ECONNREFUSED, statusRefused},
		{syscall.ENETUNREACH, statusUnreachable},
		{syscall.EHOSTUNREACH, statusUnreachable},
		{errors.New("boom"), statusOther},
	}
	for _, c := range cases {
		if got := statusFor(c.err); got != c.want {
			t.Fatalf("statusFor(%v) = %d, want %d", c.err, got, c.want)
		}
	}
}

func TestControlInfoRoundTrip(t *testing.T) {
	cases := []ControlInfo{
		{Version: "1.2.3", GOOS: "linux", GOARCH: "arm64", Hostname: "gw.example", PID: 4242},
		{Version: "dev", GOOS: "linux", GOARCH: "amd64", Hostname: "host-with-dash", PID: 1},
		{Version: `v1"weird\path`, GOOS: "linux", GOARCH: "arm64", Hostname: "a,b:c", PID: 65535},
		{Version: "", GOOS: "linux", GOARCH: "arm64", Hostname: "", PID: 0},
	}
	for _, want := range cases {
		line := want.encode()
		if line[len(line)-1] != '\n' {
			t.Fatalf("encode(%+v) has no trailing newline", want)
		}
		got, err := decodeControlInfo(strings.TrimRight(string(line), "\n"))
		if err != nil {
			t.Fatalf("decodeControlInfo(%q): %v", line, err)
		}
		if got != want {
			t.Fatalf("round trip = %+v, want %+v", got, want)
		}
	}
}

func TestDecodeControlInfoMalformed(t *testing.T) {
	for _, line := range []string{"", "not-json", `{"version":"x"`, `{"pid":"notanint"}`} {
		if _, err := decodeControlInfo(line); err == nil {
			t.Fatalf("decodeControlInfo(%q) returned no error", line)
		}
	}
}

func TestReadLine(t *testing.T) {
	got, err := readLine(strings.NewReader("TJ1\nrest"), maxLineLen)
	if err != nil {
		t.Fatal(err)
	}
	if got != "TJ1" {
		t.Fatalf("readLine = %q, want %q", got, "TJ1")
	}

	got, err = readLine(strings.NewReader("quit\r\n"), maxLineLen)
	if err != nil {
		t.Fatal(err)
	}
	if got != "quit" {
		t.Fatalf("readLine dropped no carriage return: %q", got)
	}

	if _, err := readLine(strings.NewReader("no newline"), maxLineLen); err == nil {
		t.Fatal("readLine without a newline returned no error")
	}
}
