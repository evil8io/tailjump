package embed_test

import (
	"strings"
	"testing"

	"github.com/evil8io/tailjump/internal/helper/embed"
)

func TestHelperMissing(t *testing.T) {
	_, err := embed.Helper("mips")
	if err == nil {
		t.Fatal("Helper(\"mips\") returned no error, want a missing-helper error")
	}
	if !strings.Contains(err.Error(), "linux/mips") {
		t.Fatalf("Helper(\"mips\") error = %q, want it to name linux/mips", err.Error())
	}
	if !strings.Contains(err.Error(), "task helpers") {
		t.Fatalf("Helper(\"mips\") error = %q, want it to point at task helpers", err.Error())
	}
}
