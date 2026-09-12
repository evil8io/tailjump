package manifest

import (
	"strings"
	"testing"

	"github.com/evil8io/tailjump/internal/transport"
)

func TestTransportDefaults(t *testing.T) {
	m, err := Parse([]byte("version: 1\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	ports, err := m.QUICPorts()
	if err != nil || ports != transport.DefaultPorts {
		t.Fatalf("QUICPorts = %v, %v", ports, err)
	}
	up, down, err := m.Bandwidth()
	if err != nil || up != 0 || down != 0 {
		t.Fatalf("Bandwidth = %d, %d, %v", up, down, err)
	}
}

func TestTransportKeys(t *testing.T) {
	body := "version: 1\ntransport:\n  quic_ports: \"8443-8445\"\n  bandwidth:\n    up: 20 mbps\n    down: 50 mbps\n"
	m, err := Parse([]byte(body))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	ports, err := m.QUICPorts()
	if err != nil || ports != (transport.PortRange{First: 8443, Last: 8445}) {
		t.Fatalf("QUICPorts = %v, %v", ports, err)
	}
	up, down, err := m.Bandwidth()
	if err != nil || up != 2_500_000 || down != 6_250_000 {
		t.Fatalf("Bandwidth = %d, %d, %v", up, down, err)
	}
}

func TestTransportRejectsBadValues(t *testing.T) {
	cases := map[string]string{
		"version: 1\ntransport:\n  quic_ports: \"7452-7443\"\n":                   "transport.quic_ports",
		"version: 1\ntransport:\n  bandwidth:\n    up: 20 mbps\n":                 "both up and down",
		"version: 1\ntransport:\n  bandwidth:\n    up: fast\n    down: 50 mbps\n": "transport.bandwidth.up",
	}
	for body, want := range cases {
		_, err := Parse([]byte(body))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("Parse(%q) = %v, want an error naming %q", body, err, want)
		}
	}
}
