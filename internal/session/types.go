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
}

// Session status values in the state file.
const (
	StatusStarting = "starting"
	StatusUp       = "up"
	StatusStopping = "stopping"
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
}
