package cli

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"

	"github.com/spf13/cobra"
)

const (
	rootCopyDir  = "/usr/local/libexec/tj"
	rootCopyPath = "/usr/local/libexec/tj/tj"
	sudoersPath  = "/etc/sudoers.d/tj"
	tunPath      = "/dev/net/tun"
)

func newSetupCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "setup",
		Short: "Write the sudoers rule and check the required tools",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runSetup(cmd)
		},
	}
}

func runSetup(cmd *cobra.Command) error {
	out := cmd.OutOrStdout()
	if err := checkTools(cmd); err != nil {
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

	_, _ = fmt.Fprintf(out, "installing the root copy at %s and the sudoers rule at %s\n", rootCopyPath, sudoersPath)
	_, _ = fmt.Fprintln(out, "sudo runs once and may prompt for your password")

	if err := installRoot(self, u.Username); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(out, "setup complete")
	return nil
}

// checkTools verifies the tools a session needs. systemd-run and /dev/net/tun
// are required; resolvectl is only for split and all DNS, so a missing one is
// a warning.
func checkTools(cmd *cobra.Command) error {
	out := cmd.OutOrStdout()
	if _, err := exec.LookPath("systemd-run"); err != nil {
		return fmt.Errorf("systemd-run is not on PATH; tj needs systemd to run a session")
	}
	_, _ = fmt.Fprintln(out, "systemd-run: ok")

	if _, err := os.Stat(tunPath); err != nil {
		return fmt.Errorf("%s is missing; load the tun module", tunPath)
	}
	_, _ = fmt.Fprintln(out, tunPath+": ok")

	if _, err := exec.LookPath("resolvectl"); err != nil {
		_, _ = fmt.Fprintln(out, "resolvectl: absent (split and all DNS need systemd-resolved; none works without it)")
	} else {
		_, _ = fmt.Fprintln(out, "resolvectl: ok")
	}
	return nil
}

// installRoot copies the binary and writes the sudoers rule in one privileged
// shell, validated with visudo. It removes the sudoers file when visudo
// rejects it, so a syntax error never leaves a broken rule.
func installRoot(self, username string) error {
	rule := fmt.Sprintf("%s ALL=(root) NOPASSWD: %s", username, rootCopyPath)
	script := fmt.Sprintf(`set -e
install -d -m 0755 -o root -g root %q
install -m 0755 -o root -g root %q %q
umask 077
printf '%%s\n' %q > %q
chmod 0440 %q
if ! visudo -cf %q; then rm -f %q; echo "sudoers validation failed" >&2; exit 1; fi
`, rootCopyDir, self, rootCopyPath, rule, sudoersPath, sudoersPath, sudoersPath, sudoersPath)

	cmd := exec.Command("sudo", "sh", "-c", script)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("install root copy and sudoers rule: %w", err)
	}
	return nil
}
