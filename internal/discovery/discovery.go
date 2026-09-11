// Package discovery runs the embedded POSIX sh script on a remote and
// parses its JSON result: the connected subnets, the resolvers, the cloud
// network CIDRs, and the manifest the remote advertises. See
// docs/architecture.md, "Discovery script output".
package discovery

import (
	"bytes"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
)

//go:embed discover.sh
var script string

// Cloud is the network CIDRs a cloud metadata service reports.
type Cloud struct {
	Provider string   `json:"provider"`
	Networks []string `json:"networks"`
}

// Result is the parsed discovery script output.
type Result struct {
	Version       string   `json:"version"`
	Hostname      string   `json:"hostname"`
	UnameM        string   `json:"uname_m"`
	ExecDir       string   `json:"exec_dir"`
	ManifestPath  string   `json:"manifest_path,omitempty"`
	Manifest      string   `json:"manifest,omitempty"`
	Addresses     []string `json:"addresses"`
	LinkRoutes    []string `json:"link_routes"`
	Resolvers     []string `json:"resolvers"`
	SearchDomains []string `json:"search_domains"`
	Cloud         *Cloud   `json:"cloud,omitempty"`
}

// Run pipes the embedded discovery script through exec, which runs it on
// the remote and returns its stdout, then parses and strictly validates
// the single JSON document it prints.
func Run(exec func(script string) ([]byte, error)) (*Result, error) {
	out, err := exec(script)
	if err != nil {
		return nil, fmt.Errorf("run discovery script: %w", err)
	}
	return Parse(out)
}

// Parse validates and decodes raw discovery script output.
func Parse(out []byte) (*Result, error) {
	dec := json.NewDecoder(bytes.NewReader(out))
	dec.DisallowUnknownFields()

	var res Result
	if err := dec.Decode(&res); err != nil {
		return nil, fmt.Errorf("parse discovery output: %w (output: %q)", err, truncate(out))
	}
	if dec.More() {
		return nil, errors.New("discovery script printed more than one JSON document")
	}
	if err := res.validate(); err != nil {
		return nil, err
	}
	return &res, nil
}

func (r Result) validate() error {
	if r.Version == "" {
		return errors.New("discovery result: missing version")
	}
	if r.Hostname == "" {
		return errors.New("discovery result: missing hostname")
	}
	if r.ExecDir == "" {
		return errors.New("discovery result: missing exec_dir, the remote has no writable session directory")
	}
	for _, s := range r.Addresses {
		if _, err := netip.ParsePrefix(s); err != nil {
			return fmt.Errorf("discovery result: address %q: %w", s, err)
		}
	}
	for _, s := range r.LinkRoutes {
		if _, err := netip.ParsePrefix(s); err != nil {
			return fmt.Errorf("discovery result: link route %q: %w", s, err)
		}
	}
	for _, s := range r.Resolvers {
		if _, err := netip.ParseAddr(s); err != nil {
			return fmt.Errorf("discovery result: resolver %q: %w", s, err)
		}
	}
	if r.Cloud != nil {
		for _, s := range r.Cloud.Networks {
			if _, err := netip.ParsePrefix(s); err != nil {
				return fmt.Errorf("discovery result: cloud network %q: %w", s, err)
			}
		}
	}
	if r.Manifest != "" {
		if _, err := base64.StdEncoding.DecodeString(r.Manifest); err != nil {
			return fmt.Errorf("discovery result: manifest is not valid base64: %w", err)
		}
	}
	return nil
}

// DecodedManifest base64-decodes the manifest field. It returns nil, nil
// when the remote has no manifest.
func (r Result) DecodedManifest() ([]byte, error) {
	if r.Manifest == "" {
		return nil, nil
	}
	return base64.StdEncoding.DecodeString(r.Manifest)
}

// LinkRoutePrefixes parses LinkRoutes into netip.Prefix values. validate
// already checked each entry parses, so an error here means Result was
// built outside Run or Parse.
func (r Result) LinkRoutePrefixes() ([]netip.Prefix, error) {
	return parsePrefixes(r.LinkRoutes)
}

// CloudNetworkPrefixes parses the cloud network CIDRs into netip.Prefix
// values. It returns an empty slice when Cloud is nil.
func (r Result) CloudNetworkPrefixes() ([]netip.Prefix, error) {
	if r.Cloud == nil {
		return nil, nil
	}
	return parsePrefixes(r.Cloud.Networks)
}

func parsePrefixes(cidrs []string) ([]netip.Prefix, error) {
	out := make([]netip.Prefix, 0, len(cidrs))
	for _, s := range cidrs {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return nil, fmt.Errorf("parse prefix %q: %w", s, err)
		}
		out = append(out, p)
	}
	return out, nil
}

func truncate(b []byte) []byte {
	const limit = 500
	if len(b) <= limit {
		return b
	}
	return b[:limit]
}
