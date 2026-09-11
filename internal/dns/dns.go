// Package dns has the DNS mode policy. The platform Resolver applies a mode.
package dns

// Mode is the DNS behavior of a session.
type Mode string

const (
	// ModeNone changes no DNS.
	ModeNone Mode = "none"
	// ModeSplit sends the manifest domains to the manifest servers.
	ModeSplit Mode = "split"
	// ModeAll sends every query to the manifest servers.
	ModeAll Mode = "all"
)

// Valid reports whether s names a mode.
func Valid(s string) bool {
	switch Mode(s) {
	case ModeNone, ModeSplit, ModeAll:
		return true
	default:
		return false
	}
}

// Default returns the mode to use when neither the flag nor the config sets
// one. It is split when the manifest has domains, otherwise none.
func Default(hasDomains bool) Mode {
	if hasDomains {
		return ModeSplit
	}
	return ModeNone
}
