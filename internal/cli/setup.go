package cli

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/evil8io/tailjump/internal/session"
	"github.com/evil8io/tailjump/internal/version"
)

const (
	rootCopyDir = "/usr/local/libexec/tj"
	sudoersPath = "/etc/sudoers.d/tj"
	tunPath     = "/dev/net/tun"
)

func newSetupCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "setup",
		Short: "Write the sudoers rule and check the required tools",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runSetup(cmd)
		},
	}
}

func runSetup(cmd *cobra.Command) error {
	out := cmd.OutOrStdout()
	ctx := cmd.Context()

	if err := session.CheckRootCopy(ctx); err == nil {
		_, err := fmt.Fprintf(out, "setup is current (%s)\n", version.Version)
		return err
	}

	rows := toolCheckRows()
	if err := printDoctor(cmd, rows, false); err != nil {
		return err
	}
	if err := doctorResult(rows); err != nil {
		return err
	}

	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("find own path: %w", err)
	}
	u, err := user.Current()
	if err != nil {
		return fmt.Errorf("current user: %w", err)
	}

	_, _ = fmt.Fprintf(out, "installing the root copy at %s and the sudoers rule at %s\n", session.RootCopy, sudoersPath)
	_, _ = fmt.Fprintln(out, "sudo runs once and may prompt for your password")

	if err := installRoot(self, u.Username); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(out, "setup complete")
	return nil
}

// toolCheckRows checks the local tools a session needs. Linux needs
// systemd-run to start the session unit and /dev/net/tun for the TUN
// device; macOS needs neither. tj doctor and tj setup share the rows.
func toolCheckRows() []doctorCheck {
	if runtime.GOOS != "linux" {
		return nil
	}
	return []doctorCheck{systemdRunCheck(), tunDeviceCheck()}
}

func systemdRunCheck() doctorCheck {
	if _, err := exec.LookPath("systemd-run"); err != nil {
		return failCheck("systemd-run", fmt.Errorf("systemd-run is not on PATH; tj needs systemd to run a session"))
	}
	return okCheck("systemd-run", "ok")
}

func tunDeviceCheck() doctorCheck {
	if _, err := os.Stat(tunPath); err != nil {
		return failCheck(tunPath, fmt.Errorf("%s is missing; load the tun module", tunPath))
	}
	return okCheck(tunPath, "ok")
}

// installRoot copies the binary and writes the sudoers rule in one privileged
// shell, validated with visudo. It removes the sudoers file when visudo
// rejects it, so a syntax error never leaves a broken rule.
func installRoot(self, username string) error {
	rule := fmt.Sprintf("%s ALL=(root) NOPASSWD: %s", username, session.RootCopy)
	script := fmt.Sprintf(`set -e
install -d -m 0755 -o root -g root %q
install -m 0755 -o root -g root %q %q
umask 077
printf '%%s\n' %q > %q
chmod 0440 %q
if ! visudo -cf %q; then rm -f %q; echo "sudoers validation failed" >&2; exit 1; fi
`, rootCopyDir, self, session.RootCopy, rule, sudoersPath, sudoersPath, sudoersPath, sudoersPath)

	cmd := exec.Command("sudo", "sh", "-c", script)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("install root copy and sudoers rule: %w", err)
	}
	return nil
}
