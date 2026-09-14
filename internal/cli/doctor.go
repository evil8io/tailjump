package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/evil8io/tailjump/internal/discovery"
	"github.com/evil8io/tailjump/internal/dns"
	"github.com/evil8io/tailjump/internal/helper"
	"github.com/evil8io/tailjump/internal/manifest"
	"github.com/evil8io/tailjump/internal/platform"
	"github.com/evil8io/tailjump/internal/session"
	"github.com/evil8io/tailjump/internal/sshc"
	"github.com/evil8io/tailjump/internal/version"
)

func newDoctorCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "doctor [remote]",
		Short: "Check the client, then the remote",
		Long: `tj doctor runs the client checks: the tj version, the tailnet status, the root copy, and the local DNS resolver.
tj doctor <remote> adds the remote checks: the peer, SSH, discovery, and the manifest.
It also checks the session networks, the DNS mode, the QUIC transport, and the echo socket.
Each row is ok, fail, or info, and a fail row sets the exit code to 1.`,
		Example: `  tj doctor
  tj doctor gw.example
  tj doctor tag:example --json`,
		Args:              cobra.MaximumNArgs(1),
		RunE:              runDoctor,
		ValidArgsFunction: completeRemote,
	}
	cmd.Flags().String("user", "", "the SSH user")
	cmd.Flags().Bool("json", false, "print JSON output")
	_ = cmd.RegisterFlagCompletionFunc("user", cobra.NoFileCompletions)
	return cmd
}

// Row statuses. A fail row sets the command's exit code to 1.
const (
	statusOK   = "ok"
	statusFail = "fail"
	statusInfo = "info"
)

// doctorCheck is one row of tj doctor output: a check name, a detail value,
// and a status of ok, fail, or info.
type doctorCheck struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Status string `json:"status"`
}

func infoCheck(name, value string) doctorCheck {
	return doctorCheck{Name: name, Value: value, Status: statusInfo}
}

func okCheck(name, value string) doctorCheck {
	return doctorCheck{Name: name, Value: value, Status: statusOK}
}

func failCheck(name string, err error) doctorCheck {
	return doctorCheck{Name: name, Value: err.Error(), Status: statusFail}
}

// resultCheck reports a pass/fail check: "ok" on a nil error, the error text
// on a non-nil one.
func resultCheck(name string, err error) doctorCheck {
	if err != nil {
		return failCheck(name, err)
	}
	return okCheck(name, "ok")
}

func runDoctor(cmd *cobra.Command, args []string) error {
	asJSON, _ := cmd.Flags().GetBool("json")
	flagUser, _ := cmd.Flags().GetString("user")
	ctx := cmd.Context()

	checks := clientChecks(ctx)
	if len(args) == 0 {
		return finishDoctor(cmd, checks, asJSON)
	}
	add := func(c doctorCheck) { checks = append(checks, c) }

	cfg, err := loadLocalConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	rr, err := resolveRemote(ctx, newTailnetClient(), cfg, args[0], flagUser)
	add(resultCheck("peer online", err))
	if err != nil {
		return finishDoctor(cmd, checks, asJSON)
	}
	add(infoCheck("peer", fmt.Sprintf("%s (%s)", rr.Peer.HostName, rr.Addr)))

	client, err := dialRemote(ctx, rr.Addr, rr.Peer.HostName, rr.User)
	add(resultCheck("ssh ok", err))
	if err != nil {
		return finishDoctor(cmd, checks, asJSON)
	}
	defer func() { _ = client.Close() }()

	res, err := runDiscovery(client)
	add(resultCheck("discovery ok", err))
	if err != nil {
		return finishDoctor(cmd, checks, asJSON)
	}
	add(infoCheck("manifest", valueOrAbsent(res.ManifestPath)))
	add(infoCheck("exec dir", valueOrAbsent(res.ExecDir)))

	if arch, archErr := helper.ArchForUname(res.UnameM); archErr != nil {
		add(failCheck("helper arch", archErr))
	} else {
		add(infoCheck("helper arch", arch))
	}

	m, err := decodeDoctorManifest(res)
	add(resultCheck("parse manifest", err))
	if err != nil {
		return finishDoctor(cmd, checks, asJSON)
	}

	networks, _, err := sessionNetworks(m, res, cfg, rr, nil, nil)
	if err == nil && len(networks) == 0 {
		err = fmt.Errorf("the session network list is empty")
	}
	add(resultCheck("session networks non-empty", err))
	if err == nil {
		add(infoCheck("session networks", joinOrNone(prefixStrings(networks))))
	}

	add(infoCheck("dns default mode", string(dns.Default(m.DNS != nil && len(m.DNS.Domains) > 0))))

	probe, err := doctorProbe(ctx, client, res, m, rr)
	switch {
	case err != nil:
		add(failCheck("quic transport", err))
		add(failCheck("icmp echo socket", err))
	default:
		if probe.QUICErr != nil {
			add(failCheck("quic transport", probe.QUICErr))
		} else {
			add(okCheck("quic transport", fmt.Sprintf("port %d", probe.QUICPort)))
		}
		if probe.EchoErr != nil {
			add(failCheck("icmp echo socket", probe.EchoErr))
		} else {
			add(infoCheck("icmp echo socket", probe.Echo.String()))
		}
	}

	return finishDoctor(cmd, checks, asJSON)
}

// clientChecks runs the checks that need no remote: the own version, the
// local tailnet status, the root copy, the session tools, and the local DNS
// resolver.
func clientChecks(ctx context.Context) []doctorCheck {
	checks := []doctorCheck{
		infoCheck("tj version", version.Version),
		tailnetCheck(ctx),
		rootCopyCheck(ctx),
	}
	checks = append(checks, toolCheckRows()...)
	checks = append(checks, resolverCheck())
	return checks
}

// tailnetCheck reads the local tailnet status. A dial failure names the
// socket, because tailnet.Client.Status names it in the error.
func tailnetCheck(ctx context.Context) doctorCheck {
	st, err := newTailnetClient().Status(ctx)
	if err != nil {
		return failCheck("tailnet", err)
	}
	online := 0
	for _, p := range st.Peers {
		if p.Online {
			online++
		}
	}
	return infoCheck("tailnet", fmt.Sprintf("%s, %d online peers", st.Self.HostName, online))
}

// rootCopyCheck checks the root copy through session.CheckRootCopy, which in
// one call checks the file, the version, and the sudo rule. connect runs
// in-process as root and never uses the root copy there, so the row is
// informational when the effective uid is 0.
func rootCopyCheck(ctx context.Context) doctorCheck {
	if os.Geteuid() == 0 {
		return infoCheck("root copy", "not needed as root")
	}
	if err := session.CheckRootCopy(ctx); err != nil {
		return failCheck("root copy", err)
	}
	return okCheck("root copy", version.Version)
}

// resolverCheck reports whether the platform DNS resolver service is
// available. This row replaces the earlier dns mode availability row.
func resolverCheck() doctorCheck {
	if platform.New().Resolver.Available() {
		return infoCheck("systemd-resolved", "available: split and all work")
	}
	return infoCheck("systemd-resolved", "not available: only all works")
}

// doctorProbe brings the QUIC transport up through a temporary helper and
// tears it down, so the report shows whether a UDP packet reaches the range
// on the remote, and asks the helper which socket it has for ICMP echo.
func doctorProbe(ctx context.Context, client *sshc.Client, res *discovery.Result, m *manifest.Manifest, rr *resolvedRemote) (session.ProbeResult, error) {
	goarch, err := helper.ArchForUname(res.UnameM)
	if err != nil {
		return session.ProbeResult{}, err
	}
	ports, err := m.QUICPorts()
	if err != nil {
		return session.ProbeResult{}, err
	}
	up, down, err := m.Bandwidth()
	if err != nil {
		return session.ProbeResult{}, err
	}
	return session.Probe(ctx, client, goarch, rr.Addr, ports, up, down)
}

func decodeDoctorManifest(res *discovery.Result) (*manifest.Manifest, error) {
	body, err := res.DecodedManifest()
	if err != nil {
		return nil, err
	}
	return decodeManifest(body)
}

func valueOrAbsent(s string) string {
	if s == "" {
		return "absent"
	}
	return s
}

func printDoctor(cmd *cobra.Command, checks []doctorCheck, asJSON bool) error {
	if asJSON {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(checks)
	}
	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
	for _, c := range checks {
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\n", displayStatus(c.Status), c.Name, c.Value)
	}
	return w.Flush()
}

// displayStatus uppercases a fail status, so a failing row stands out in the
// human table.
func displayStatus(status string) string {
	if status == statusFail {
		return "FAIL"
	}
	return status
}

// finishDoctor prints the report, then fails the command when one or more
// checks failed, so a script that runs tj doctor sees a non-zero exit.
func finishDoctor(cmd *cobra.Command, checks []doctorCheck, asJSON bool) error {
	if err := printDoctor(cmd, checks, asJSON); err != nil {
		return err
	}
	return doctorResult(checks)
}

func doctorResult(checks []doctorCheck) error {
	n := 0
	for _, c := range checks {
		if c.Status == statusFail {
			n++
		}
	}
	if n == 0 {
		return nil
	}
	plural := "s"
	if n == 1 {
		plural = ""
	}
	return &ExitError{Code: 1, Err: fmt.Errorf("%d check%s failed", n, plural)}
}
