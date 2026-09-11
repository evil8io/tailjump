package sshc

import (
	"bufio"
	"bytes"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"
)

// knownHosts is a per-hostname host key store at <cacheDir>/known_hosts.
// Each line is "hostname keytype base64key", the ssh.PublicKey wire format
// used by OpenSSH authorized_keys, prefixed with the hostname.
type knownHosts struct {
	path string
}

func newKnownHosts(cacheDir string) *knownHosts {
	return &knownHosts{path: filepath.Join(cacheDir, "known_hosts")}
}

// callback returns a HostKeyCallback for hostname. Tailscale SSH authenticates
// the node over WireGuard, so the client trusts any key on first sight: it
// stores a new key, and only warns on stderr when a stored key changes. The
// connection continues in both cases.
func (k *knownHosts) callback(hostname string) ssh.HostKeyCallback {
	return func(_ string, _ net.Addr, key ssh.PublicKey) error {
		stored, err := k.lookup(hostname)
		if err != nil {
			fmt.Fprintf(os.Stderr, "tj: warning: read known_hosts: %v\n", err)
		}
		if stored == nil {
			if err := k.store(hostname, key); err != nil {
				fmt.Fprintf(os.Stderr, "tj: warning: store host key for %s: %v\n", hostname, err)
			}
			return nil
		}
		if !bytes.Equal(stored.Marshal(), key.Marshal()) {
			fmt.Fprintf(os.Stderr, "tj: warning: host key for %s changed\n", hostname)
		}
		return nil
	}
}

func (k *knownHosts) lookup(hostname string) (ssh.PublicKey, error) {
	f, err := os.Open(k.path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 3 || fields[0] != hostname {
			continue
		}
		key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(strings.Join(fields[1:], " ")))
		if err != nil {
			return nil, fmt.Errorf("parse stored key for %s: %w", hostname, err)
		}
		return key, nil
	}
	return nil, scanner.Err()
}

func (k *knownHosts) store(hostname string, key ssh.PublicKey) error {
	if err := os.MkdirAll(filepath.Dir(k.path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(k.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	line := fmt.Sprintf("%s %s", hostname, strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))))
	_, err = fmt.Fprintln(f, line)
	return err
}
