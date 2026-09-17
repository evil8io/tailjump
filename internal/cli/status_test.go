package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/evil8io/tailjump/internal/session"
)

// upState is a session that is up, with the metrics of one sample.
func upState() *session.State {
	return &session.State{
		Remote:    "gw.example",
		Addr:      "100.64.0.10",
		User:      "root",
		Networks:  []string{"10.0.0.0/16"},
		DNS:       session.PlanDNS{Mode: "split"},
		StartedAt: time.Now().Add(-time.Minute).UTC().Format(time.RFC3339),
		Status:    session.StatusUp,
		Transport: session.TransportQUIC,
		QUICPort:  7443,
		Protocols: "tcp,udp,icmp",
		Metrics: &session.Metrics{
			UpdatedAt: time.Now().UTC().Format(time.RFC3339),
			RTTMS:     31.4,
			UpBytes:   325_058_560,
			DownBytes: 2_254_857_830,
			UpRate:    150_000,
			DownRate:  1_050_000,
		},
	}
}

// TestWriteStatusPrintsTheMetrics checks the rows of a session with metrics:
// the latency of the path, the round-trip time, and the traffic.
func TestWriteStatusPrintsTheMetrics(t *testing.T) {
	var buf bytes.Buffer
	path := &pathInfo{Type: pathDirect, Endpoint: "203.0.113.5:41641", LatencyMS: 31.2}
	if err := writeStatus(&buf, upState(), path); err != nil {
		t.Fatalf("write status: %v", err)
	}

	out := buf.String()
	for _, want := range []string{
		"Path:",
		"direct 31ms",
		"RTT:",
		"31ms",
		"Traffic:",
		"up 1.2 mbps, down 8.4 mbps (310 MiB up, 2.1 GiB down)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status has no %q:\n%s", want, out)
		}
	}
}

// TestWriteStatusWithoutMetrics checks that a state file without metrics, one
// an older session wrote, prints the other rows and no metrics row.
func TestWriteStatusWithoutMetrics(t *testing.T) {
	st := upState()
	st.Metrics = nil

	var buf bytes.Buffer
	if err := writeStatus(&buf, st, nil); err != nil {
		t.Fatalf("write status: %v", err)
	}

	out := buf.String()
	for _, absent := range []string{"RTT:", "Traffic:"} {
		if strings.Contains(out, absent) {
			t.Errorf("status has %q without metrics:\n%s", absent, out)
		}
	}
	for _, want := range []string{"Transport:", "Path:", "Networks:"} {
		if !strings.Contains(out, want) {
			t.Errorf("status has no %q:\n%s", want, out)
		}
	}
}

// TestStatusJSONHasTheMetrics checks that --json carries the metrics and the
// path, because statusJSON embeds the state file.
func TestStatusJSONHasTheMetrics(t *testing.T) {
	var buf bytes.Buffer
	path := &pathInfo{Type: pathDirect, Endpoint: "203.0.113.5:41641", LatencyMS: 31.2}
	if err := json.NewEncoder(&buf).Encode(statusJSON{State: upState(), Path: path}); err != nil {
		t.Fatalf("encode status: %v", err)
	}

	var got struct {
		Status  string `json:"status"`
		Metrics *struct {
			UpdatedAt string  `json:"updated_at"`
			RTTMS     float64 `json:"rtt_ms"`
			UpBytes   uint64  `json:"up_bytes"`
			DownRate  uint64  `json:"down_rate"`
		} `json:"metrics"`
		Path *pathInfo `json:"path"`
	}
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if got.Status != session.StatusUp || got.Path == nil || got.Path.LatencyMS != 31.2 {
		t.Fatalf("status json = %+v", got)
	}
	if got.Metrics == nil {
		t.Fatal("status json has no metrics")
	}
	if got.Metrics.RTTMS != 31.4 || got.Metrics.UpBytes != 325_058_560 || got.Metrics.DownRate != 1_050_000 {
		t.Errorf("metrics json = %+v", *got.Metrics)
	}
	if got.Metrics.UpdatedAt == "" {
		t.Error("the metrics json has no time")
	}
}

func TestFormatRTT(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{0.42, "0.4ms"},
		{9.94, "9.9ms"},
		{10, "10ms"},
		{31.4, "31ms"},
		{1234.5, "1234ms"},
	}
	for _, c := range cases {
		if got := formatRTT(c.in); got != c.want {
			t.Errorf("formatRTT(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}
