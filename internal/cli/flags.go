package cli

import (
	"errors"
	"time"

	"github.com/spf13/cobra"

	"github.com/evil8io/tailjump/internal/dns"
	"github.com/evil8io/tailjump/internal/protocols"
	"github.com/evil8io/tailjump/internal/transport"
)

// dnsModeValue is the pflag.Value for --dns and for defaults.dns in tj
// config set. The zero value's String is empty, so an unset flag still
// reads as "not set" in the precedence chain, for example dnsMode.
type dnsModeValue struct {
	value string
}

func (v *dnsModeValue) String() string { return v.value }

func (v *dnsModeValue) Set(s string) error {
	if !dns.Valid(s) {
		return errors.New("want none, split, or all")
	}
	v.value = s
	return nil
}

func (v *dnsModeValue) Type() string { return "none|split|all" }

// Values returns the valid --dns values, for the completion of S11.
func (v *dnsModeValue) Values() []string { return []string{"none", "split", "all"} }

// transportModeValue is the pflag.Value for --transport and for
// defaults.transport in tj config set.
type transportModeValue struct {
	value string
}

func (v *transportModeValue) String() string { return v.value }

func (v *transportModeValue) Set(s string) error {
	if !transport.Valid(s) {
		return errors.New("want auto, quic, or ssh")
	}
	v.value = s
	return nil
}

func (v *transportModeValue) Type() string { return "auto|quic|ssh" }

// Values returns the valid --transport values, for the completion of S11.
func (v *transportModeValue) Values() []string { return []string{"auto", "quic", "ssh"} }

// protocolSetValue is the pflag.Value for --protocols and for
// defaults.protocols in tj config set.
type protocolSetValue struct {
	value string
}

func (v *protocolSetValue) String() string { return v.value }

func (v *protocolSetValue) Set(s string) error {
	if _, err := protocols.Parse(s); err != nil {
		return err
	}
	v.value = s
	return nil
}

func (v *protocolSetValue) Type() string { return "tcp,udp,icmp" }

// Values returns the valid --protocols names, for the completion of S11.
func (v *protocolSetValue) Values() []string {
	return []string{protocols.TCP, protocols.UDP, protocols.ICMP}
}

// reconnectForValue is the pflag.Value for --reconnect-for and for
// defaults.reconnect_for and remotes.<name>.reconnect_for in tj config set
// and tj alias set.
type reconnectForValue struct {
	value string
}

func (v *reconnectForValue) String() string { return v.value }

func (v *reconnectForValue) Set(s string) error {
	if d, err := time.ParseDuration(s); err != nil || d < 0 {
		return errors.New("want a Go duration such as 10m, or 0")
	}
	v.value = s
	return nil
}

func (v *reconnectForValue) Type() string { return "duration" }

// flagString returns the current value of a flag that Var registered, for
// example the enum types above, whose Value.String carries it. GetString
// does not work here, because it requires Value.Type to be "string".
func flagString(cmd *cobra.Command, name string) string {
	return cmd.Flags().Lookup(name).Value.String()
}
