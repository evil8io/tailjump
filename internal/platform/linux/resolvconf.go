//go:build linux

package linux

import (
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
)

// applyResolvConfFallback rewrites /etc/resolv.conf with the servers and the
// search domains, after it backs up the current file so Revert can restore
// it.
func (r *Resolver) applyResolvConfFallback(servers []netip.Addr, domains []string) error {
	if err := r.backupResolvConf(); err != nil {
		return err
	}
	content := renderResolvConf(servers, domains)
	if err := os.Remove(r.resolvConfPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s: %w", r.resolvConfPath, err)
	}
	if err := os.WriteFile(r.resolvConfPath, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", r.resolvConfPath, err)
	}
	return nil
}

// backupResolvConf records how to restore the current resolv.conf. A
// symlink keeps only its target, in linkFile; a regular file is copied
// whole, to backupFile.
func (r *Resolver) backupResolvConf() error {
	if err := os.MkdirAll(filepath.Dir(r.linkFile), 0o755); err != nil {
		return fmt.Errorf("create runtime dir: %w", err)
	}
	fi, err := os.Lstat(r.resolvConfPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat %s: %w", r.resolvConfPath, err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(r.resolvConfPath)
		if err != nil {
			return fmt.Errorf("read symlink %s: %w", r.resolvConfPath, err)
		}
		if err := os.WriteFile(r.linkFile, []byte(target), 0o600); err != nil {
			return fmt.Errorf("record resolv.conf symlink target: %w", err)
		}
		return nil
	}
	data, err := os.ReadFile(r.resolvConfPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", r.resolvConfPath, err)
	}
	if err := os.WriteFile(r.backupFile, data, 0o600); err != nil {
		return fmt.Errorf("backup %s: %w", r.resolvConfPath, err)
	}
	return nil
}

// revertResolvConfFallback restores resolv.conf from whichever record
// backupResolvConf left, and removes that record, so a second call finds
// neither and does nothing.
func (r *Resolver) revertResolvConfFallback() error {
	target, hasLink, err := readIfExists(r.linkFile)
	if err != nil {
		return err
	}
	backup, hasBackup, err := readIfExists(r.backupFile)
	if err != nil {
		return err
	}
	if !hasLink && !hasBackup {
		return nil
	}
	if err := os.Remove(r.resolvConfPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s: %w", r.resolvConfPath, err)
	}
	if hasLink {
		if err := os.Symlink(string(target), r.resolvConfPath); err != nil {
			return fmt.Errorf("restore symlink %s: %w", r.resolvConfPath, err)
		}
		return os.Remove(r.linkFile)
	}
	if err := os.WriteFile(r.resolvConfPath, backup, 0o644); err != nil {
		return fmt.Errorf("restore %s: %w", r.resolvConfPath, err)
	}
	return os.Remove(r.backupFile)
}

func readIfExists(path string) ([]byte, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("read %s: %w", path, err)
	}
	return data, true, nil
}

func renderResolvConf(servers []netip.Addr, domains []string) string {
	var b strings.Builder
	b.WriteString("# written by tj, restored on disconnect\n")
	if len(domains) > 0 {
		b.WriteString("search " + strings.Join(domains, " ") + "\n")
	}
	for _, s := range servers {
		b.WriteString("nameserver " + s.String() + "\n")
	}
	return b.String()
}
