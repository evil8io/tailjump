package dns

import "testing"

func TestDefault(t *testing.T) {
	if Default(true) != ModeSplit {
		t.Fatal("domains present must default to split")
	}
	if Default(false) != ModeNone {
		t.Fatal("no domains must default to none")
	}
}

func TestValid(t *testing.T) {
	for _, s := range []string{"none", "split", "all"} {
		if !Valid(s) {
			t.Fatalf("%q must be valid", s)
		}
	}
	if Valid("bogus") {
		t.Fatal("bogus must be invalid")
	}
}
