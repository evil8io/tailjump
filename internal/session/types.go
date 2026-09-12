// Package session has the session plan, the session state, and the connect
// and disconnect flows. This file holds the shared types; a later chunk adds
// the flows.
package session

// PlanDNS is the DNS part of a plan and a state.
type PlanDNS struct {
	Mode    string   `json:"mode"`
	Servers []string `json:"servers"`
	Domains []string `json:"domains"`
}

// Plan is the JSON that tj connect hands to the root session runner.
type Plan struct {
	Remote     string   `json:"remote"`
	Addr       string   `json:"addr"`
	User       string   `json:"user"`
	Networks   []string `json:"networks"`
	DNS        PlanDNS  `json:"dns"`
	HelperArch string   `json:"helper_arch"`
	// Transport is the mode: auto, quic, or ssh. An empty value means auto.
	Transport string `json:"transport"`
	// QUICPorts is the helper's listen range, for example 7443-7452.
	QUICPorts string `json:"quic_ports"`
	// BandwidthUp and BandwidthDown are the Brutal rates in bytes per second,
	// zero for BBR.
	BandwidthUp   uint64 `json:"bandwidth_up"`
	BandwidthDown uint64 `json:"bandwidth_down"`
	// Controller overrides the controller rule on both sides. Only cubic is
	// valid, as a measurement knob; empty means the rule.
	Controller string `json:"controller,omitempty"`
	// Protocols is the set the client forwards, for example "tcp,udp,icmp".
	// An empty value means all three.
	Protocols string `json:"protocols"`
}

// Session status values in the state file.
const (
	StatusStarting = "starting"
	StatusUp       = "up"
	StatusStopping = "stopping"
)

// Transport values in the state file.
const (
	TransportQUIC = "quic"
	TransportSSH  = "ssh"
)

// State is the JSON that the running session writes for tj status.
type State struct {
	Remote    string   `json:"remote"`
	Addr      string   `json:"addr"`
	User      string   `json:"user"`
	Networks  []string `json:"networks"`
	DNS       PlanDNS  `json:"dns"`
	StartedAt string   `json:"started_at"`
	PID       int      `json:"pid"`
	Status    string   `json:"status"`
	// Transport is quic or ssh. QUICPort is the helper's port on quic.
	// Fallback is the reason when auto ended on ssh.
	Transport string `json:"transport"`
	QUICPort  uint16 `json:"quic_port,omitempty"`
	Fallback  string `json:"fallback,omitempty"`
	// Protocols is the set the session forwards.
	Protocols string `json:"protocols"`
}
