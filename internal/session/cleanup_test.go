package session

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/evil8io/tailjump/internal/platform"
	"github.com/evil8io/tailjump/internal/platform/fake"
)

func TestCleanupIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	writeSampleState(t, dir)
	if err := writePlan(filepath.Join(dir, planFile), []byte(`{}`)); err != nil {
		t.Fatalf("write plan: %v", err)
	}

	dev := &fake.Device{}
	resolver := &fake.Resolver{}
	router := &fake.Router{}
	plat := platform.Platform{
		Device:   dev,
		Resolver: resolver,
		Router:   router,
		Paths:    &fake.Paths{RuntimeDirValue: dir},
	}

	for i := 0; i < 2; i++ {
		if err := cleanup(plat); err != nil {
			t.Fatalf("cleanup call %d: %v", i+1, err)
		}
	}

	if len(dev.DeleteCalls) != 2 || dev.DeleteCalls[0] != deviceName || dev.DeleteCalls[1] != deviceName {
		t.Fatalf("want two device deletes of %q, got %v", deviceName, dev.DeleteCalls)
	}
	if len(resolver.RevertCalls) != 2 || resolver.RevertCalls[0] != deviceName || resolver.RevertCalls[1] != deviceName {
		t.Fatalf("want two dns reverts of %q, got %v", deviceName, resolver.RevertCalls)
	}
	if router.ResetCalls != 2 {
		t.Fatalf("want two route resets, got %d", router.ResetCalls)
	}
	if _, err := os.Stat(StatePath(dir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("want the state file gone, got err=%v", err)
	}
	if _, err := os.Stat(PlanPath(dir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("want the plan file gone, got err=%v", err)
	}
}

func TestCleanupToleratesErrorsAndNoState(t *testing.T) {
	plat := platform.Platform{
		Device:   &fake.Device{DeleteErr: errors.New("link busy")},
		Resolver: &fake.Resolver{RevertErr: errors.New("resolved unreachable")},
		Router:   &fake.Router{ResetErr: errors.New("rules busy")},
		Paths:    &fake.Paths{RuntimeDirValue: t.TempDir()},
	}

	if err := cleanup(plat); err != nil {
		t.Fatalf("cleanup must tolerate a device and a resolver error, got %v", err)
	}
}
