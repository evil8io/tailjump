package discovery

import (
	"bytes"
	"os"
	"os/exec"
	"testing"
)

// TestScriptRunsLocallyUnderSh executes the embedded script on the local
// machine, not over SSH, so it needs no network and no real gateway. It
// only proves the script is valid POSIX sh that prints one JSON document
// Parse accepts; it does not check specific field values, which depend on
// the local network.
func TestScriptRunsLocallyUnderSh(t *testing.T) {
	for _, tool := range []string{"sh", "ip", "uname", "base64"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not found on PATH, skipping the local script smoke test", tool)
		}
	}

	res, err := Run(func(s string) ([]byte, error) {
		cmd := exec.Command("sh")
		cmd.Stdin = bytes.NewReader([]byte(s))
		var out, stderr bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			t.Logf("script stderr: %s", stderr.String())
			return out.Bytes(), err
		}
		return out.Bytes(), nil
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Hostname == "" || res.UnameM == "" {
		t.Fatalf("want a populated result, got %+v", res)
	}
}

func testRemoteAddr(t *testing.T) string {
	t.Helper()
	addr := os.Getenv("TJ_TEST_REMOTE")
	if addr == "" {
		t.Skip("TJ_TEST_REMOTE not set, skipping a test that dials a real gateway")
	}
	return addr
}
