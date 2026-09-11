//go:build darwin

package darwin

import (
	"path/filepath"
	"testing"
)

func TestPathsDirs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	p := NewPaths()
	if got, want := p.ConfigDir(), filepath.Join(home, "Library", "Application Support", "tj"); got != want {
		t.Errorf("ConfigDir() = %q, want %q", got, want)
	}
	if got, want := p.CacheDir(), filepath.Join(home, "Library", "Caches", "tj"); got != want {
		t.Errorf("CacheDir() = %q, want %q", got, want)
	}
	if got, want := p.RuntimeDir(), "/var/run/tj"; got != want {
		t.Errorf("RuntimeDir() = %q, want %q", got, want)
	}
}

func TestHomeDirFallback(t *testing.T) {
	t.Setenv("HOME", "")
	if got := homeDir(); got != "" {
		t.Errorf("homeDir() with empty HOME = %q, want empty", got)
	}
}
