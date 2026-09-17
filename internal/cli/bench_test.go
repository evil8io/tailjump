package cli

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

// TestBenchDurationFlag locks in the default and the range of --duration:
// 1s to 30s in whole seconds, the range the helper accepts.
func TestBenchDurationFlag(t *testing.T) {
	f := newBenchCmd().Flags().Lookup("duration")
	if f == nil {
		t.Fatal("bench is missing --duration")
	}
	if f.DefValue != "5s" {
		t.Fatalf("--duration default = %s, want 5s", f.DefValue)
	}
	cases := []struct {
		value time.Duration
		ok    bool
	}{
		{0, false},
		{500 * time.Millisecond, false},
		{1500 * time.Millisecond, false},
		{31 * time.Second, false},
		{time.Second, true},
		{5 * time.Second, true},
		{30 * time.Second, true},
	}
	for _, c := range cases {
		err := checkBenchDuration(c.value)
		if (err == nil) != c.ok {
			t.Fatalf("checkBenchDuration(%s) = %v, want ok=%v", c.value, err, c.ok)
		}
	}
}

// TestBenchTransportFlag locks in that --transport takes the same values as
// on connect.
func TestBenchTransportFlag(t *testing.T) {
	f := newBenchCmd().Flags().Lookup("transport")
	if f == nil {
		t.Fatal("bench is missing --transport")
	}
	if f.Value.Type() != "auto|quic|ssh" {
		t.Fatalf("--transport type = %s, want auto|quic|ssh", f.Value.Type())
	}
	for _, value := range []string{"auto", "quic", "ssh"} {
		if err := f.Value.Set(value); err != nil {
			t.Fatalf("--transport %s: %v", value, err)
		}
	}
	if err := f.Value.Set("derp"); err == nil {
		t.Fatal("--transport derp was accepted")
	}
}

// quicBenchOutput is the report of a run on the QUIC transport, with the
// numbers of the human example.
func quicBenchOutput() benchOutput {
	return benchOutput{
		Remote:    "gw.example",
		Addr:      "100.64.0.10",
		Transport: "quic",
		QUICPort:  7443,
		Path:      &pathInfo{Type: pathDirect, LatencyMS: 12},
		Up:        benchLeg{Bytes: 133169152, Seconds: 5.0, Rate: 26500000},
		Down:      benchLeg{Bytes: 311427072, Seconds: 5.1, Rate: 62250000},
	}
}

func TestPrintBenchHuman(t *testing.T) {
	cmd := newBenchCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := printBench(cmd, quicBenchOutput(), false); err != nil {
		t.Fatalf("printBench: %v", err)
	}
	want := "Remote:     gw.example (100.64.0.10)\n" +
		"Transport:  quic (port 7443)\n" +
		"Path:       direct 12ms\n" +
		"Up:         212 mbps (127 MiB in 5.0s)\n" +
		"Down:       498 mbps (297 MiB in 5.1s)\n"
	if out.String() != want {
		t.Fatalf("printBench wrote\n%s\nwant\n%s", out.String(), want)
	}
}

// TestPrintBenchFallbackLine locks in that a fallback run names the reason,
// in the wording of tj status.
func TestPrintBenchFallbackLine(t *testing.T) {
	report := quicBenchOutput()
	report.Transport, report.QUICPort, report.Fallback = "ssh", 0, "the handshake to udp port 7443 did not complete"

	cmd := newBenchCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := printBench(cmd, report, false); err != nil {
		t.Fatalf("printBench: %v", err)
	}
	want := "Transport:  ssh (fallback: the handshake to udp port 7443 did not complete)\n"
	if !bytes.Contains(out.Bytes(), []byte(want)) {
		t.Fatalf("printBench wrote\n%s\nwant a line\n%s", out.String(), want)
	}
}

func TestPrintBenchJSON(t *testing.T) {
	cmd := newBenchCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := printBench(cmd, quicBenchOutput(), true); err != nil {
		t.Fatalf("printBench: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal %s: %v", out.String(), err)
	}
	for _, key := range []string{"remote", "addr", "transport", "quic_port", "path", "up", "down"} {
		if _, ok := got[key]; !ok {
			t.Fatalf("key %q is missing from %s", key, out.String())
		}
	}
	if _, ok := got["fallback"]; ok {
		t.Fatalf("key fallback is present without a fallback: %s", out.String())
	}
	up, ok := got["up"].(map[string]any)
	if !ok {
		t.Fatalf("up is not an object: %s", out.String())
	}
	if up["bytes"] != float64(133169152) || up["seconds"] != 5.0 || up["rate"] != float64(26500000) {
		t.Fatalf("up = %v, want the bytes, the seconds, and the rate of the run", up)
	}
}

// TestPrintBenchJSONOmitsQUICPort locks in that a run on the SSH transport
// prints the fallback reason and no port.
func TestPrintBenchJSONOmitsQUICPort(t *testing.T) {
	report := quicBenchOutput()
	report.Transport, report.QUICPort, report.Fallback = "ssh", 0, "no free udp port"

	cmd := newBenchCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := printBench(cmd, report, true); err != nil {
		t.Fatalf("printBench: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal %s: %v", out.String(), err)
	}
	if _, ok := got["quic_port"]; ok {
		t.Fatalf("key quic_port is present on the ssh transport: %s", out.String())
	}
	if got["fallback"] != "no free udp port" {
		t.Fatalf("fallback = %v, want the reason", got["fallback"])
	}
}
