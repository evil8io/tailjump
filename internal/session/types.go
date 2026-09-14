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
	Remote string `json:"remote"`
	// Ref is the reference of the remote after the alias lookup, a hostname
	// or a tag. The unit resolves it again on a reconnect.
	Ref        string   `json:"ref"`
	Addr       string   `json:"addr"`
	User       string   `json:"user"`
	Networks   []string `json:"networks"`
	DNS        PlanDNS  `json:"dns"`
	HelperArch string   `json:"helper_arch"`
	// ManifestSHA256 is the hex SHA-256 of the raw manifest bytes that
	// connect read, the hash of zero bytes when the remote has no manifest.
	ManifestSHA256 string `json:"manifest_sha256"`
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
	// SingleLane keeps the SSH transport on the primary lane alone. It is a
	// measurement knob; connect sets it from TJ_SSH_LANES.
	SingleLane bool `json:"single_lane,omitempty"`
	// Verbose sets the session log level to debug. The unit inherits no flag
	// and no environment, so tj -v connect puts the value here.
	Verbose bool `json:"verbose,omitempty"`
	// ReconnectFor is the reconnect window in seconds, 0 for off.
	ReconnectFor int `json:"reconnect_for"`
}

// Session status values in the state file.
const (
	StatusStarting     = "starting"
	StatusUp           = "up"
	StatusReconnecting = "reconnecting"
	StatusStopping     = "stopping"
)

// Transport values in the state file.
const (
	TransportQUIC = "quic"
	TransportSSH  = "ssh"
)

// ReconnectState is the progress of a session that lost its transport and
// rebuilds it. tj status prints it, and tj connect reads the attempt count.
type ReconnectState struct {
	// Since is the RFC 3339 time of the loss.
	Since    string `json:"since"`
	Attempts int    `json:"attempts"`
	// Reason is the loss, or the failure of the last attempt.
	Reason string `json:"reason"`
}

// State is the JSON that the running session writes for tj status.
type State struct {
	Remote string `json:"remote"`
	// Ref is the reference the session resolves, a hostname or a tag. tj
	// connect matches it while a reconnect moves the address.
	Ref       string   `json:"ref"`
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
	// Lanes are the SSH connections that opened, in the order tcp, udp,
	// icmp, dns. It is empty on the QUIC transport.
	Lanes string `json:"lanes,omitempty"`
	// Reconnect is set while the status is reconnecting.
	Reconnect *ReconnectState `json:"reconnect,omitempty"`
	// Reconnects counts the losses the session recovered from. It survives a
	// reconnect, so tj status reports the whole session.
	Reconnects int `json:"reconnects,omitempty"`
}
