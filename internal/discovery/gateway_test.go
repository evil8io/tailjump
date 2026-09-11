package discovery

import (
	"context"
	"net/netip"
	"os"
	"os/user"
	"testing"

	"github.com/evil8io/tailjump/internal/sshc"
)

// TestRunAgainstRealGateway runs discovery over a real SSH connection. It
// reads the target from TJ_TEST_REMOTE and skips when it is unset, so CI
// without a gateway passes. See CLAUDE.md, "Public repository hygiene".
func TestRunAgainstRealGateway(t *testing.T) {
	addr, err := netip.ParseAddr(testRemoteAddr(t))
	if err != nil {
		t.Fatalf("TJ_TEST_REMOTE: %v", err)
	}

	sshUser := os.Getenv("TJ_TEST_USER")
	if sshUser == "" {
		if u, err := user.Current(); err == nil {
			sshUser = u.Username
		} else {
			sshUser = "root"
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), sshc.DialTimeout)
	defer cancel()
	client, err := sshc.Dial(ctx, addr, "tj-discovery-test", sshUser, t.TempDir())
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = client.Close() }()

	res, err := Run(func(script string) ([]byte, error) {
		return client.Run("sh", []byte(script))
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if res.ExecDir == "" {
		t.Fatal("want a non-empty exec_dir on the shared gateway")
	}
	if len(res.LinkRoutes) == 0 {
		t.Fatal("want at least one link route on the shared gateway")
	}
	if res.Cloud == nil || res.Cloud.Provider != "aws" {
		t.Fatalf("want an aws cloud block on the shared gateway, got %+v", res.Cloud)
	}
}
