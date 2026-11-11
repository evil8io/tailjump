# Architecture

This document contains the decisions that are open in `docs/spec.md`. A change to a decision here is a PR on this document first.

## Module and layout

The module is `github.com/evil8io/tailjump`. The Go version is 1.27. CGO is off on every target.

## Deviations from the spec

1. **No `tailscale.com` dependency.** `tailscale.com/client/local` is in the spec. That module depends on the whole Tailscale tree. The tree contains a pinned gVisor and a wireguard-go fork. The data plane needs its own gVisor version. Two pinned gVisor versions in one build are a conflict. `tj` reads the local API over the unix socket with `net/http` instead. The request is `GET http://local-tailscaled.sock/localapi/v0/status` with the header `Sec-Tailscale: localapi`. tj dials only the Linux socket `/var/run/tailscale/tailscaled.sock`. tj does not have a macOS socket path.
2. **TUN device from `golang.zx2c4.com/wireguard/tun`.** The data plane wraps `tun.Device` in a gVisor link endpoint. tj does not import another wireguard-go package. The `tun` package depends on `conn` and `rwcancel` of the same module.


| Path | Content |
| -- | -- |
| `cmd/tj` | the CLI entry point |
| `cmd/tjhelper` | the helper entry point; the same code as `tj _remote`, built small for `linux/amd64` and `linux/arm64` |
| `internal/cli` | the cobra commands |
| `internal/config` | the local config file |
| `internal/manifest` | the manifest schema, the merge with discovery, and the session network computation; pure, no I/O |
| `internal/discovery` | the embedded POSIX `sh` script and its JSON result |
| `internal/tailnet` | the peers from the tailscaled local API |
| `internal/sshc` | the SSH client: dial, exec, banner, host key store, keepalive |
| `internal/mux` | the client side and the helper side of the mux protocol, on the SSH transport and on the QUIC transport; the helper's echo socket and error queue |
| `internal/transport` | the transport mode, the QUIC port range, the rate parser, and the controller choice; standard library only |
| `internal/protocols` | the protocol set of a session: which of TCP, UDP, and ICMP echo the client forwards; standard library only |
| `internal/congestion` | the BBR and Brutal controllers, copied from hysteria (MIT, see `LICENSE.hysteria`), and the function that applies one to a connection |
| `internal/helper` | the remote side (`tj _remote`); `internal/helper/embed` has the built helper binaries |
| `internal/dataplane` | the TUN device, the netstack, and the flow handler |
| `internal/dns` | the DNS mode logic; the platform applies it |
| `internal/session` | the plan, the state file, the lock, the connect and disconnect flows |
| `internal/platform` | the interfaces; `linux`, `darwin`, and `fake` implementations |
| `internal/version` | the version string, set by ldflags |
| `docs` | the spec, this document, the manifest schema, the spikes |
| `test/e2e` | the container rig and the measurement scripts, against the remotes of `test/e2e/target.env` |

## Build

1. `task helpers` builds `cmd/tjhelper` for `linux/amd64` and `linux/arm64` with `-trimpath -ldflags "-s -w"` into `internal/helper/embed/bin/tj-helper-linux-<arch>`.
2. `task build` runs `task helpers`, then builds `cmd/tj` for the host into `bin/tj`.
3. `all:bin` is embedded in `internal/helper/embed`. The directory contains a `.gitkeep`. The binaries are in `.gitignore`. A missing helper binary is a runtime error at `connect`, not a build error, so `go build ./...`, `go vet ./...`, and `go test ./...` work without `task helpers`.
4. goreleaser runs `task helpers` in `before.hooks` and builds `linux/amd64`, `linux/arm64`, and `darwin/arm64`. The version is set from the git tag through `-X github.com/evil8io/tailjump/internal/version.Version=`.
5. `cmd/tjhelper` does not import a package that imports `tailscale.com`, cobra, gvisor, or `log/slog`. It imports yamux, the quic-go fork `github.com/apernet/quic-go`, `crypto/tls`, `golang.org/x/sys/unix` for the socket error queue, and `internal/congestion`. `encoding/json` is in the helper graph because of the fork's TLS fingerprint feature. tj does not use that feature. On 2026-09-12, with `-trimpath -ldflags "-s -w"`, the measured helper size was 6.50 MiB for linux/arm64 and 7.03 MiB for linux/amd64. The gate is 8 MiB, and `task helpers` fails at or above it. A second large dependency does not fit under the gate.
6. The fork does not have a usable module tag, because its semver tags contain the upstream module path. The pin is a pseudo-version of a `vX.Y.Z-mod-rename` branch, and Renovate cannot follow it. A bump needs the head commit of the newest branch, because Go reads the branch name as a version. The reason for the fork is `Conn.SetCongestionControl`, which upstream does not export; see `docs/spikes/05-quic-transport.md`.

## Platform interfaces

`internal/platform/platform.go` has one interface per concern. The Linux implementation is in `internal/platform/linux`. The macOS implementation is in `internal/platform/darwin`. The in-memory implementation is in `internal/platform/fake`. The real implementation is selected by build tags, and `platform.New()` returns it. A change may extend an interface. The fake is then changed in the same PR. `linux` and `darwin` cannot import this package, because this package already imports them; a method whose interface signature names a type from here, for example `Logs` and `LogOptions`, reaches `linux.Runner` and `darwin.Runner` as plain arguments instead, and `platform_linux.go` and `platform_darwin.go` each wrap the concrete Runner to restore the interface shape.

```go
type Device interface {
    // Create creates the TUN device with the name and the MTU.
    // It returns the wireguard tun.Device and the real name.
    Create(name string, mtu int) (tun.Device, string, error)
    // Configure sets the addresses and brings the device up.
    Configure(name string, addrs []netip.Prefix) error
    Delete(name string) error
}

type Router interface {
    Add(device string, prefixes []netip.Prefix) error
    Remove(device string, prefixes []netip.Prefix) error
    // Reset removes every session route and rule that Add installed,
    // whether or not the device still exists. It is safe to call twice.
    Reset() error
    // Connected returns the connected subnets of the client,
    // without the loopback and without the tj device.
    Connected() ([]netip.Prefix, error)
}

type Resolver interface {
    // Available reports whether the platform resolver service runs.
    Available() bool
    ApplySplit(device string, servers []netip.Addr, domains []string) error
    ApplyAll(device string, servers []netip.Addr, domains []string) error
    // Revert removes every DNS change for the device. It is safe to call twice.
    Revert(device string) error
}

type Runner interface {
    // Start starts the session process detached with the plan file as its argument.
    Start(plan string) error
    Stop() error
    Active() (bool, error)
    // Logs writes the session log to w, per opts. It stops when ctx ends and
    // returns nil, not the process error, because the caller asked for the stop.
    Logs(ctx context.Context, w io.Writer, opts LogOptions) error
}

// LogOptions selects the lines Runner.Logs writes: Lines is the last N lines,
// 0 with a non-zero Since meaning every line since Since; Follow keeps
// writing new lines until ctx ends; Since zero means no time limit.
type LogOptions struct {
    Lines  int
    Follow bool
    Since  time.Time
}

type Paths interface {
    ConfigDir() string  // Linux: $XDG_CONFIG_HOME/tj; macOS: ~/Library/Application Support/tj
    CacheDir() string   // Linux: $XDG_CACHE_HOME/tj; macOS: ~/Library/Caches/tj
    RuntimeDir() string // Linux: /run/tj; macOS: /var/run/tj
}
```

The fake records every call and returns configured errors. The tests of `session` and `dns` use the fake only.

## The remote side

### Upload and start

The client opens two SSH session channels on the primary connection:

1. Upload: `d="${XDG_RUNTIME_DIR:-$HOME/.cache/tj}"; mkdir -p "$d" && f="$d/tj-helper.$$" && cat > "$f" && chmod 0700 "$f" && echo "$f"`, which Tailscale SSH runs with `/bin/bash -c`. The client writes the helper for the remote's architecture to stdin, closes stdin, and reads the path from stdout. The upload has a limit of 30 s, because an upload once stayed open for 22 minutes on a stalled Tailscale path. After the limit, the client closes the SSH connection. The command then fails with an error about the upload.
2. Run: `exec <path> _remote`. Stdin and stdout are the mux transport. Stderr is the helper log. The client writes it to its own log.

On the SSH transport, the session opens one more SSH connection per extra lane. Each extra lane has its own helper process and its own yamux session over the stdin and stdout of that helper. A lane is one protocol of the set, `tcp`, `udp`, or `icmp`, or the session's DNS servers. The primary lane is the connection of item 2 above. The control stream, the QUIC negotiation, and the `quit`, `icmp`, and `unlink` verbs are on the primary lane. The primary lane is also the lane of the first protocol of the set, in the order `tcp`, `udp`, `icmp`. A session has at most four lanes: one per protocol and one for the DNS servers. A session never has a lane per flow. The client starts every lane with the same exec of the same file. This is possible because Linux allows the exec of a file that another process already runs. The client thus uploads the helper once and does not make a copy. On the QUIC transport, the session does not open an extra lane.

A flow is sent on the lane of its protocol. A flow to one of the session's DNS servers on port 53, over UDP or over TCP, is sent on the dns lane. A query thus does not wait behind a bulk flow of the same protocol. When the lane of a protocol did not open, the flows of that protocol are sent on the primary lane. The dns lane exists with `--dns split` and with `--dns all`. With `--dns none`, the session does not have DNS servers. The session then does not open a dns lane, and such a flow is sent on its protocol lane.

The helper keeps its own file at start. It removes the file when the client sends the control verb `unlink`, and at its exit. The client sends `unlink` on the primary control stream right after the transport selection. On the QUIC transport, the client sends it at once. On the SSH transport, the client sends it after the handshake of every extra lane finishes, whether the handshake succeeded or failed. It waits for the answer and logs a warning when the remove failed. The file exists during the connect phase only. The helper ignores SIGPIPE. Otherwise, the Go runtime ends a process on a write to a closed stdout, without its deferred remove. With the signal ignored, the write fails and the mux closes. The helper then removes the file on its exit path. These results were measured on the shared gateway on 2026-09-12. Tailscale SSH kills the command's process tree at once when the client connection drops. A helper that was idle at that moment still removed its file at exit. A file stays behind on two paths: a SIGKILL of the helper and a power loss on the remote. At start, every helper thus removes each `tj-helper.*` file in its own directory whose modification time is older than 10 minutes. It never removes its own file. `tj doctor` starts a temporary helper and sends `quit`. That helper removes its file at its exit, so the doctor does not send `unlink`. At exit, the helper also removes `$HOME/.cache/tj` when that directory is empty. Tailscale SSH does not set `XDG_RUNTIME_DIR`, so the cache path is the common case. This was verified on the shared gateway on 2026-09-11.

The architecture map is `x86_64` to `amd64` and `aarch64` to `arm64`. Any other value is the error "unsupported remote architecture".

### Mux protocol v5 on the SSH transport

The SSH transport is the helper's stdin (client to helper) and stdout (helper to client). The client uploads the helper that is embedded in its own binary, so the client and the helper always have the same version. The protocol number is a guard, not a negotiation.

1. Handshake: the helper writes the line `TJ5\n` to stdout at start. The client waits at most 10 s for that line. Any other line before it is an error message.
2. Then both sides run yamux over the transport: the client is `yamux.Client`, the helper is `yamux.Server`. The yamux config is `EnableKeepAlive` on, `KeepAliveInterval` 5 s, `ConnectionWriteTimeout` 20 s, and `MaxStreamWindowSize` 4 MiB. `MaxStreamWindowSize` is the only mux setting with a measured throughput effect in spike 1. The keepalive is the liveness check of the session. The mux is `github.com/hashicorp/yamux`. The alternative was `github.com/libp2p/go-yamux` v5, which tunes its receive window by itself and has a memory manager. With the fixed 4 MiB window, both libraries had the same throughput on the measured paths on 2026-09-11, so the smaller dependency is kept. The numbers of that comparison are not recorded.
3. The client opens every stream. The first byte of a stream is its kind: `0` control, `1` TCP, `2` UDP, `3` probe, `4` ICMP echo, `5` bench. The helper answers a probe stream with the status byte `0` and a close. The client opens a probe stream on the QUIC transport right after the QUIC handshake.
4. A TCP, UDP, or ICMP echo stream continues with the destination: 1 byte address length (4 or 16), the address bytes, and 2 bytes port, big-endian. On an echo stream the port field is the ICMP identifier. The helper answers with 1 byte status: `0` ok, `1` refused, `2` unreachable, `3` timeout, `4` other, `5` unsupported. On a status other than `0`, the helper closes the stream. On a TCP stream the client waits for the status byte before it sends data. On a UDP stream and on an echo stream the client does not wait; see the UDP item.
5. TCP: after the status byte, the stream is the connection. A `Close` on one side is a half-close. The other side reads EOF and can still write.
6. UDP: the client does not wait for the status byte. It sends the destination and then the first datagram frame at once, to save a round trip. A frame is 2 bytes length, big-endian, then the body. Both sides send frames. A request frame body is 1 byte TTL, then the datagram. The helper sets the TTL on its socket before the send, so a traceroute probe keeps its TTL. A reply frame body starts with 1 byte kind: `0` a datagram, then the payload; `1` an ICMP error, then 1 byte type, 1 byte code, 4 bytes info (the MTU of a Packet Too Big), 1 byte address length, the address of the sender of the error, and the bytes of the original datagram that are quoted in the error. The helper has one UDP socket per stream. The socket is connected to the destination, with `IP_RECVERR` or `IPV6_RECVERR` on. The helper reads the ICMP errors from the socket's error queue. The flow continues after an error. On a dial error it writes a non-zero status byte and closes, which the client treats as the end of the flow. Each side closes the stream after an idle time: 60 s by default, and 10 s when the destination port is 53. A stream close ends the flow. In a measurement on 2026-09-11, the status wait cost one round trip: 61 ms against 31 ms for a DNS query on a 29 ms path.
7. ICMP echo: the client opens one stream per pinged address and identifier. One `ping` process is thus one flow, and its sequence numbers never mix with the sequence numbers of another `ping` process on the remote. The client sends the destination and the first request frame at once. A request frame body is 2 bytes sequence, 1 byte TTL, then the echo payload. The helper answers the status byte first: `0`, or `5` when it does not have an echo socket. After a `5`, the helper closes the stream. The client then drops every echo request of the session, with one warning. A reply frame body starts with 1 byte kind: `0` an echo reply, then 2 bytes sequence, 4 bytes round-trip time in microseconds as the helper measured it, and the payload; `1` an ICMP error with the layout of the UDP item, where the quoted bytes start at the original ICMP header. The helper opens, in this order, a raw ICMP socket, which needs `CAP_NET_RAW`, then a ping socket. A ping socket is `SOCK_DGRAM` with `IPPROTO_ICMP` or `IPPROTO_ICMPV6`, and it depends on the sysctl `net.ipv4.ping_group_range`. On a raw socket, the helper sets a random identifier, filters the replies and the errors by it, and parses the errors from the messages themselves. On a ping socket, the kernel sets the identifier and delivers only the matching replies. The helper then reads the errors from the error queue. The helper sets the TTL per request. The idle time is 60 s on both sides.
8. Bench: `tj bench` opens one stream per direction. After the kind byte, the stream has 1 byte direction, `0` up or `1` down, and 2 bytes duration in seconds, big-endian, 1 to 30. Up means the client sends; down means the helper sends. The helper answers with 1 byte status: `0` ok, or `4` other for an invalid request. On `4` it closes the stream. On an up run the helper discards what it reads until EOF, with a read deadline of the duration plus 10 s. It then writes 8 bytes big-endian with the byte count it received. The client's clock runs until that count arrives, so the drain of the transport buffers counts as part of the measurement. On a down run the helper writes 32 KiB blocks of zeros for the duration, then closes. The client counts the bytes until EOF. The 30 s cap on the duration is a limit on the load that a client can put on the remote. `tj bench` uses the same stream kind on the SSH transport and on the QUIC transport.
9. Control: the client opens exactly one control stream, as the first stream. The helper writes one JSON line: `{"version":"…","goos":"linux","goarch":"…","hostname":"…","pid":…}`. The client can write the line `quit\n`. The helper then closes its QUIC listener and exits. The helper also exits when the transport closes. The client can write the line `icmp\n`, and the helper answers `icmp raw|ping|none [<low>-<high>]\n` with the echo socket it can open and the `ping_group_range` value. `tj doctor` reports that answer. The client can write the line `unlink\n`. The helper then removes its own file and answers `unlink ok\n`, or `unlink <reason>\n` when the remove failed. The client thus gets the confirmation that the file is deleted before the session reports up. The helper of an extra lane does not receive these verbs: the client uses an extra lane for flows, for the liveness wait, and for `quit` only. The other control verbs are the QUIC negotiation below.

The helper dials with a 10 s timeout from the remote's default source address. It does not open a listener other than the QUIC listener of the transport below.

### Transport v2: QUIC over the tailnet

Every TCP connection and UDP flow of a session runs as a stream of one QUIC connection between the client and the helper. The QUIC connection is over UDP to the remote's IPv4 tailnet address. Tailscale SSH stays the bootstrap, the only trust anchor, the control stream, and the fallback transport. The reason is packet loss. One TCP connection for every flow has head-of-line blocking and one shared congestion window, so every flow stalls on one lost packet. QUIC recovers per packet and keeps each stream independent. Under 7% loss in the rig, the bulk rate of the QUIC transport was 5.3 times the bulk rate of the SSH transport. See `docs/spikes/06-loss-test.md`. A collapse on a relayed path once looked like relay loss, but its cause was the routing loop of `docs/spikes/08-routing-loop.md`. The numbers are from the spike `docs/spikes/05-quic-transport.md`.

The client and the helper negotiate on the control stream, after the info line:

1. The client writes `quic <bind-address> <first-port>-<last-port> <controller> <client-fingerprint>\n`. The bind address is the tailnet address the client dialed for SSH. The port range is from the manifest `transport.quic_ports`, default `7443-7452`, because the helper cannot read the manifest. The controller is `bbr`, or `brutal=<bytes-per-second>` with the helper's send rate from the manifest `transport.bandwidth.down`. The fingerprint is `sha256:<hex>` of the client's DER certificate.
2. The helper generates a self-signed ECDSA P-256 certificate in memory. It binds the first free UDP port of the range on the bind address only, and never on the wildcard address. It then starts the QUIC listener and writes `quic <port> <helper-fingerprint>\n`. When the helper cannot bind a port of the range, it writes `quic-unavailable <reason>\n`. The session then uses the SSH transport.
3. The client dials `<bind-address>:<port>` with its certificate, ALPN `tj/5`, TLS 1.3, and a verify function that accepts only the helper certificate with the pinned fingerprint. The helper requires a client certificate and accepts only the pinned client fingerprint. It refuses every handshake once a connection exists.
4. Right after the handshake the client opens a probe stream, kind `3`, and waits for the status byte. With TLS 1.3, the client can complete the handshake before the helper verifies the client certificate. The client thus treats only an answered probe as proof that the helper accepted the connection. The dial, the probe, and the answer share one 5 s budget.
5. When that budget passes without an answered probe, the client writes `quic-abandon\n` and the helper closes the listener. The session then uses the SSH transport, with one warning that contains the policy rule, `udp:<first>-<last>`.
6. The helper accepts one QUIC connection per session. Both certificates are in memory only. The helper does not write a file.

The QUIC configuration is the same on both sides:

* `InitialPacketSize` is 1232, and `DisablePathMTUDiscovery` is on. These two settings are mandatory, because the tailnet path MTU is 1280. With the library default of 1280 bytes of payload, a handshake does not complete in either direction, measured in spike 5.
* `MaxIdleTimeout` is 15 s.
* `KeepAlivePeriod` is 5 s.
* `MaxIncomingStreams` and `MaxIncomingUniStreams` are 65536, because the library default of 100 is too low for a tunnel.
* 0-RTT is off.
* Datagrams are off.
* Connection migration is not used.

The receive windows are 8 MiB per stream and 20 MiB per connection on both sides, for the initial value and for the maximum. In spike 6, the throughput under 7% loss through a tj session was 22% to 55% higher than with the library defaults. In spike 5, the throughput on a raw stream did not increase. The UDP socket buffers are the library behaviour: the fork asks for 8 MiB with `SO_RCVBUFFORCE`. Both root processes get that size, as measured in spike 6.

Congestion control: both sides install the controller right after the handshake with the fork's `Conn.SetCongestionControl`. BBR is the default. When the manifest contains `transport.bandwidth`, Brutal is selected. The client sends at `up`, and the helper sends at `down`. Brutal sends at the configured rate and compensates loss, which is unfair on a shared relay, so it stays opt-in. In spike 5, the measured rate was 261.3 Mbit/s for BBR against 1.7 Mbit/s for Cubic, at 7% loss and 30 ms delay.

Flows over QUIC: the client opens every stream. The first byte is the kind: `1` TCP, `2` UDP, `3` probe, or `4` ICMP echo. Kind `0` is refused, because the control stream stays on the SSH transport. The destination, the status byte, the frames, and the idle timeouts are the same as on the SSH transport. The helper dials the same way. A TCP flow's `Close` on a QUIC stream closes the send side only, and the peer reads EOF. A second `Close` at the end of the flow releases the read side with `CancelRead`. `internal/dataplane` depends on a `mux.Dialer`. The transport behind the dialer is not visible to `internal/dataplane`.

Session lifecycle: the transport is selected right after the mux is up and before the device exists, so `--transport quic` fails early. The session opens the extra lanes after that selection, and only when the selection ends on the SSH transport. A QUIC session thus does not have a cost for the lanes. The lanes open in parallel, and each lane has the yamux keepalive of 5 s. When an extra lane does not open, one warning with the lane and the reason is logged. The flows of its protocol are then sent on the primary lane. A lane does not reconnect. The yamux keepalive of the primary lane is still the liveness check of the session. When a yamux session closes on any lane, the session ends by the same path as for a closed primary mux. When the QUIC connection closes, the session ends by that same path. A dead path is known within 15 s on the QUIC transport, and within 25 s on the SSH transport. The write timeout on the primary lane's yamux session is 20 s, not 10 s. The reason is a measurement of the loss job: with the old 10 s timeout, there was one keepalive timeout in 20 minutes at 7% loss on that connection, while a QUIC session ran a saturated download. The QUIC idle timer never fired in the same run. A nearly idle TCP connection under loss stalls through RTO backoff, and a busy lane does not. On the stop path, the session closes the QUIC connection, sends `quit` on every lane's control stream, and closes every lane's mux and SSH connection. The helper closes its listener before it exits. It also closes the listener when the SSH transport closes. The state file contains `transport`, `quic_port`, `fallback`, and `lanes`. `tj status` prints `quic (port N)`, `ssh, lanes tcp,udp,icmp,dns`, or `ssh (fallback: <reason>), lanes tcp,udp,icmp,dns`. `tj doctor` brings the transport up through a temporary helper, reports `ok, port N` or the reason, and tears it down.

Selection: the precedence is `tj connect --transport auto|quic|ssh`, then `remotes.<name>.transport`, then `defaults.transport`. The default is `auto`. With `quic`, the connect fails when the transport is unavailable, and the session does not fall back. `quic` is the test mode. With `ssh`, the client skips the negotiation.

The cost of the fallback was measured in spike 9, on a client on 2026-09-12, before the lanes existed. With a bulk download in the session, a DNS query took up to 795 ms on the SSH transport over the relayed path, and up to 355 ms over the direct path. On QUIC, the values were 74 ms and 60 ms for the same two paths. The cause was the one TCP connection with the 4 MiB yamux window, in which every flow was queued behind the bulk flow. The lanes were measured in spike 10, on the same client and remotes on the same day. Under load, a DNS query and an ICMP echo take at most 31 ms on the direct path, which is the idle round-trip time. On the relayed path, they take at most 147 ms. A TCP connect next to the session also takes up to 147 ms on that path, so the remaining latency there is the relay queue. BBR on the QUIC transport keeps the relay queue short. A lane cannot remove it. The bulk rate and the idle latency do not change. Four lanes add at most one second to the connect. `docs/spikes/09-tj-against-sshuttle.md` and `docs/spikes/10-ssh-lanes.md` have the tables.

Security: the listener binds the tailnet address only, so it is not reachable from the VPC interface or from the internet. Access to the port range depends on one UDP rule per remote in the tailnet policy, for example `{"src": ["group:example"], "dst": ["tag:example"], "ip": ["udp:7443-7452"]}`. Without that rule, the session falls back. Mutual TLS pins both certificates by their fingerprints. The fingerprints are sent over the already-authenticated SSH channel, so tj does not use a second trust anchor. The helper accepts one connection per session. Keys and certificates are per session and in memory. The listener closes on `quic-abandon`, on `quit`, and when the SSH transport closes.

Invariants: the tailnet range is excluded from the session networks in every version, so the client's UDP socket to the tailnet address is never routed through `tj0`. The QUIC transport forwards exactly what the SSH transport forwards: the flows to the session networks. It does not add reachability. After a session, on every exit path, the remote does not contain a file, a listener, or a process of that session.

## The session

### Privilege on Linux

`tj setup` copies the running binary to `/usr/local/libexec/tj/tj` (root, 0755) and writes `/etc/sudoers.d/tj` with `<user> ALL=(root) NOPASSWD: /usr/local/libexec/tj/tj`. On Linux it checks `systemd-run` and `/dev/net/tun`, then runs `sudo` interactively once, and validates the file with `visudo -c`. When `session.CheckRootCopy` already passes, `setup` prints `setup is current (<version>)` and returns without the tool checks and without sudo.

The root copy is root-owned, so the file in the NOPASSWD rule is not user-writable. `tj connect` compares the output of `/usr/local/libexec/tj/tj version` with its own version, and refuses on a mismatch with the message to run `tj setup`.

The root copy and the session unit do not run a command from the user config, for example a notification hook. The config file is user-writable, so with such a hook, user input runs as root. A desktop notification belongs in a process of the user.

### Connect flow

1. The unprivileged `tj connect` resolves the remote, opens SSH, reads the manifest, runs discovery, and computes the session networks. It refuses on an empty set. It refuses when a session to another address is active, unless `--replace` is set. A session to the address of the plan is the already-up case in "CLI conventions".
2. It writes the plan as JSON to the stdin of `sudo -n /usr/local/libexec/tj/tj _session start`. When the effective uid is 0, it runs the same code in-process without sudo.
3. `_session start` writes the plan to `/run/tj/plan.json` (0600) and starts the unit: `systemd-run --unit tj-session --collect --property KillMode=mixed --property TimeoutStopSec=20 --property "ExecStopPost=<root tj> _session cleanup" <root tj> _session run /run/tj/plan.json`. With `--foreground`, it runs `_session run` in-process instead.
4. `_session run` opens SSH, uploads and starts the helper, opens the mux, and selects the transport. It then opens the extra lanes on the SSH transport, creates the device, adds the routes, and applies the DNS mode. It writes the state file with status `up` and waits for a signal or a loss. The final exit is a signal or a reconnect that gives up. On the final exit, it reverts the DNS, removes the routes and the device, and closes the QUIC connection. It then sends `quit` to the helper of every lane and removes the state file.
5. A loss is a mux close, a lane close, a QUIC connection close, or a failed resume probe. With `reconnect_for` 0, a loss ends the session, the way a signal does. The unit then logs `session ended: <reason>`, or `session ended: lane mux closed` with the lane name. With `reconnect_for` above 0 a loss starts a reconnect instead. The unit logs `session lost: <reason>`, clears the switch dialer, closes the transport, reverts the DNS, and writes status `reconnecting`. The switch dialer is the `mux.Dialer` in front of the data plane. It fails every new dial while a transport does not exist. A new flow thus gets a reset or an unreachable answer at once, instead of a hang. An open flow ends with its stream. The device, the routes, and the netstack stay. The unit closes the SSH connection before the mux, for two reasons. A `Close` on an SSH channel does not end a pending read. A yamux `Close` waits for its receive loop.
6. On each attempt, the unit resolves the remote again and follows a move. See "Remote resolution". After a dial to a new address, and before the helper upload, the unit reads the manifest and compares its SHA-256 hash with the plan's `manifest_sha256`. On a mismatch, the unit ends with `remote changed`. The user then runs `tj connect` again. The reconnect keeps the transport of the session. When a QUIC session does not get QUIC on an attempt, it logs `quic transport unavailable: <reason>` as a failed attempt and retries. An SSH session selects SSH without a negotiation.
7. The backoff starts at 1 s and doubles to a cap of 10 s. The window is `reconnect_for`, default 10 minutes. It starts at the loss, on the Go monotonic clock, which stops during a suspend, so only awake time counts toward the window. After a successful reconnect, the unit logs `session reconnected after <duration> (attempt <n>)` and ends the window. A later loss starts a new reconnect with a full window. After the window passes, the unit logs `reconnect gave up after <duration>` and exits the way a loss without a reconnect window does. The reconnect loop runs inside `_session run`, not through systemd `Restart=`. On a restart, systemd runs the `ExecStopPost` cleanup and starts a new process. The device, the routes, and the netstack that a reconnect keeps are then removed.
8. The unit uses a resume probe to find a loss without a close. Such a loss occurs because a client that suspends can lose its transport without a close on either side. A ticker reads the wall clock every second. A gap above 5 s between two reads is a suspend. One probe then follows, with a 3 s limit: a yamux ping on the primary lane, or a QUIC probe stream. A failed probe is a loss. The detector runs only when the reconnect window is above 0.
9. A stop signal takes precedence over a reconnect at once: it cancels the backoff wait, the resolve call, and the dial. The `ExecStopPost` cleanup does not change. The reconnect code is platform-neutral. It builds on macOS. Its behaviour on macOS is unverified, like the rest of the session runner.
10. `tj connect` prints `connecting to <hostname> (<addr>) as <user>` on stderr, right after the remote resolution. `_session start` then waits up to 60 s for the state file with status `up`, or for the unit to fail. It does not print anything on success. `tj connect` reads the state file after that wait and prints one line on stdout: `session to <remote> up: <transport>, dns <mode>, <n> networks, <elapsed>`. The elapsed time runs from the start of the command. With `-v` the root copy follows the session log on stderr during the wait, from the start of the unit. The steps of the unit then print as they happen. Without `-v` the header line and the final line are the whole output of a connect that succeeds. When the unit fails, or when the wait times out, the root copy prints the last 20 lines of this session's log on stderr. The error then ends with `see tj logs for the full log`. With `-v` there is no tail, because the stream already printed those lines. A signal during that wait cancels the connect. `tj connect` sends SIGINT to `sudo` instead of a kill signal, and sudo relays it to the root copy. The root copy stops the unit and waits up to 25 s until the unit is inactive. It then prints `connect cancelled; the session is stopped` and exits 130. sudo 1.9.14 and later run the root copy in a pseudo-terminal, so a Ctrl-C can reach that child alone. `tj connect` then reads the exit code 130 of sudo as the interrupt, and exits 130 itself. A signal after the session came up does not have an effect: the session stays up.
11. `tj disconnect` runs `sudo -n /usr/local/libexec/tj/tj _session stop`, which runs `systemctl stop tj-session`. It reads the remote from the state file before the stop, and prints `session to <remote> ended` on stdout.
12. `_session cleanup` runs after every stop. It reverts the DNS on `tj0`, restores `/etc/resolv.conf` from the backup, and deletes `tj0` when it exists. It then removes the session rules, flushes the session table, and removes the state and plan files. Every step is safe to repeat.

On Linux the session logs to the journal of the unit `tj-session`. On macOS, it logs to `RuntimeDir/session.log` (0640). `Start` opens that file with `O_TRUNC`, so the file contains the log of the last session only. `tj logs` prints it on both platforms through `Runner.Logs`. An unprivileged caller reaches it through `sudo -n <root copy> _session logs`, the way `tj disconnect` reaches `_session stop`.

The plan JSON has this format: `{"remote":…,"addr":…,"user":…,"ref":…,"networks":[…],"dns":{"mode":…,"servers":[…],"domains":[…]},"helper_arch":…,"manifest_sha256":…,"transport":"auto|quic|ssh","quic_ports":"7443-7452","bandwidth_up":0,"bandwidth_down":0,"protocols":"tcp,udp,icmp","single_lane":false,"verbose":false,"reconnect_for":600}`. The bandwidths are bytes per second, zero for BBR. An empty `protocols` means all three. With `single_lane`, the session keeps the SSH transport on the primary lane. `tj connect` sets `single_lane` from `TJ_SSH_LANES`. With `verbose`, the log level of the session is debug. `tj -v connect` sets `verbose`, because the unit does not inherit a flag or the environment. `ref` is the reference the unit resolves again on a reconnect, a hostname or a tag. `manifest_sha256` is the hex SHA-256 of the manifest bytes that connect read. On a reconnect after a move, the unit compares it, to guard against a changed remote. `reconnect_for` is the reconnect window in seconds, 0 for off.

The state file `/run/tj/session.json` (0644) has this format: `{"remote":…,"addr":…,"user":…,"ref":…,"networks":[…],"dns":{…},"started_at":…,"pid":…,"status":"starting|up|reconnecting|stopping","transport":"quic|ssh","quic_port":…,"fallback":…,"protocols":"tcp,udp,icmp","lanes":"tcp,udp,icmp,dns","reconnect":{"since":…,"attempts":…,"reason":…},"reconnects":0}`. `lanes` contains the SSH connections that opened, in the order tcp, udp, icmp, dns. It is empty on the QUIC transport. `ref` is the reference the session resolves again on a reconnect. `reconnect` contains the progress of a loss while the status is `reconnecting`: the RFC 3339 time of the loss, the attempt count, and the reason for the loss or the last failed attempt. `reconnects` is the number of losses that the session recovered from. Its value is kept across a reconnect. `tj status` reads the file without root and prints `Protocols:`. It also prints `Reconnects:` when the value is above zero.

### Session metrics

While the status is up, a sampler probes the current transport once a second. The probe is a yamux ping on the SSH transport, or one probe stream on the QUIC transport. A failed probe is not a loss. The mux keepalive and the resume detector already find a dead transport, so one lost probe under load is a normal event. The sampler sends its first sample right after the first probe, about a second after the session comes up. It then sends one sample every 5 s.

A sample has the mean round-trip time of the probes that got an answer in the interval. The value is absent when the interval does not have an answered probe. It also has the byte totals since the session start, and the rates of the interval in bytes per second. Up is the count of the packets that the client read from the TUN device. Down is the count of the packets that the client wrote to the device.

The sampler does not own a state of the runner. It sends each sample on a channel. The runner goroutine is the only writer of the state file, because that goroutine already writes the state on every other session event. `runner.wait` reads the sample from that channel and calls `writeState` before it waits again. On a loss, the round-trip time and the rates are set to zero. While the session reconnects, it does not move traffic and does not have a transport to probe. The totals stay, because the data plane stays, and they continue across the reconnect for the same reason.

The cost of the sampler is one probe a second on the transport. `writeState` writes a temp file next to the state file and renames it over the state file. The rename is necessary, because `tj status` and the wait of `tj connect` read the file while the session writes it every 5 s. With a write that truncates the file, a reader can get a partial document.

### Protocol set

`tj connect --protocols tcp,udp,icmp` selects the protocols that the client forwards. The precedence is the flag, then `remotes.<name>.protocols`, then `defaults.protocols`. The default is all three. The set is a comma-separated list of `tcp`, `udp`, and `icmp` in any order. Only the client enforces the set. A disabled protocol does not get a forwarder in the netstack. The netstack thus answers TCP with a reset and UDP with a port unreachable. The pump drops an ICMP echo request. The wire protocol and the helper do not change with the set. With `--dns split` and `--dns all`, the queries go through the tunnel. `connect` thus refuses these modes when the set does not contain `udp`, and it names the conflict. `--dns none` works with every set. The set does not have a manifest key. What the helper reaches depends on the remote's security group and the operator's policy on the remote. `tj describe` prints the effective set from the config. `tj doctor` prints the echo socket of the remote.

The lock is the unit `tj-session.service`. The refusal reads the remote and the start time from the state file, for the message only. macOS uses the pid file `runner.pid` in `RuntimeDir` instead of a unit.

### Device and routes

The device is `tj0` with MTU 1500. Its addresses are `169.254.117.1/32`, `fd00:117::1/128`, and `fe80::1/64`. The global-scope ULA `fd00:117::1/128` is required as an IPv6 source. Per RFC 6724, a link-local source is rejected for a global destination. Without the ULA, a client without global IPv6 thus does not have a source at all. This was measured on 2026-09-11. The session adds one `dev tj0` route per session network, IPv4 and IPv6. The session also adds a host route for each DNS server that is outside the session networks, so the queries are captured.

On Linux, the routes are in the routing table 117, behind the rule `pref 5300 lookup 117` for IPv4 and for IPv6, and not in the main table. tailscaled marks its own UDP packets with the bypass mark `0x80000`. With its rules 5210 to 5250, the kernel sends a marked packet to the main table, then to the default table, then to unreachable. With a session route in main, tailscaled's packets to an endpoint inside the session networks are captured. The remote advertises its own VPC address as a WireGuard endpoint. The discovery pings thus travel through the tunnel, and magicsock switches to that path. That path then contains its own transport. The path blackholes, so magicsock falls back to DERP. The cycle repeats every 15 to 20 s. In a measurement on a client on 2026-09-12, DNS answered 14 of 33 queries with the routes in main. With the remote's address excluded, DNS answered 24 of 24 queries. See `docs/spikes/08-routing-loop.md`. The rule is after tailscaled's rule 5270 and before main at 32766, so every unmarked packet still reaches the session routes. The remote's own address also stays reachable through the tunnel. `Router.Reset` flushes the table and deletes the rules on every exit path, because the kernel removes the routes on a device delete, but not the rules. On macOS the routes stay in the main table, because tailscaled binds its sockets to the default interface there.

The session samples tailscaled's endpoint for the remote every 10 s and logs one warning when the path changes three or more times within two minutes. A loop flaps every 15 to 20 s. A direct-path discovery changes the path once. An endpoint inside the session networks is not a fault on its own: a remote with a global VPC IPv6 address has its real direct endpoint inside the routed /56. This was measured on 2026-09-12. The loop cannot be reproduced in the rig, because tailscaled runs outside the rig's network namespace. On a client, the warning is thus the check for a regression.

### Netstack

The netstack is gVisor `stack.New` with the ipv4, ipv6, tcp, udp, and icmp protocols. NIC 1 is a link endpoint fed from the TUN device. With `SetPromiscuousMode(1, true)` and `SetSpoofing(1, true)`, the stack accepts every destination and answers from the destination address. The route table has a default route for IPv4 and one for IPv6 through NIC 1.

`tcp.NewForwarder` handles a new TCP flow. It opens a mux TCP stream. On a non-zero status, it completes the request with a reset. On a zero status, it creates the endpoint and copies both ways with half-close. `udp.NewForwarder` handles a new UDP flow, keyed by the 4-tuple. It opens one mux UDP stream per flow and converts the datagrams to and from frames. On the idle timeout, it closes both sides.

These implementation notes were measured against the pinned gvisor on 2026-09-11. Read and write the TUN device in batches of `dev.BatchSize()`, which is 128 on Linux. With a one-buffer loop, the rest of a GRO super-packet is dropped. With batched writes, the device can coalesce the netstack's segments into one kernel write. Allocate 128 buffers of `offset+65535`, with the read and write offset at least 10 for the virtio-net header, because `CreateTUN` enables GSO. The NIC needs both `SetPromiscuousMode` and `SetSpoofing`. It does not need an `AddProtocolAddress` call. Handle each TCP `ForwarderRequest.CreateEndpoint` in its own goroutine, because it runs the handshake and blocks the dispatch path. The UDP `CreateEndpoint` runs inline. In the pinned gvisor, the handler of the UDP forwarder is `func(*udp.ForwarderRequest) bool`. Return true, or the stack sends an ICMP port-unreachable. Release both `pkt.ToView()` and `pkt.DecRef()`, or the buffer pool grows.

The netstack does not have a forwarder for ICMP echo. The TUN pump reads the protocol field before the injection. It takes an ICMPv4 echo request, type 8, and an ICMPv6 echo request, type 128, out of the netstack path. A fragment and a packet with an IPv6 extension header stay on the netstack path. The pump keys a flow by the pinged address and the identifier, and queues the request. The flow's goroutine opens the echo stream and sends the sequence, the TTL, and the payload. For a reply, the client builds the echo reply packet from the pinged address to the original source. The packet is an IPv4 or IPv6 header plus ICMP, with the checksums. The client queues the packet on the link endpoint, so the TUN write pump writes it with the packets of the netstack. Before the flow existed, a measurement on a client on 2026-09-12 had 6 echo requests and 0 replies. The netstack itself did not send a reply. gVisor's `RawFactory` was the alternative for the capture. The pump check does not need a raw endpoint or an extra goroutine per packet, so the pump check is kept.

Traceroute works through the same paths. For a UDP flow, the UDP endpoint exposes the TTL of each captured datagram through `SetReceiveTTL` and `SetReceiveHopLimit`. The frame to the helper contains the TTL. An ICMP error the helper returns, a Time Exceeded from a hop or a Destination Unreachable, becomes a packet the client builds: the outer header from the sender of the error to the original source, the ICMP error header with the type, the code, and the info word, and the original packet rebuilt from the flow, the IP header and the UDP header with the ports and the length of the last datagram, or the IP header and the quoted ICMP bytes, cut to 576 bytes for IPv4 and 1280 bytes for IPv6. The application's kernel then matches the error to the socket by the quoted headers, as it does for a real error. This covers `traceroute` in its UDP and ICMP modes, `mtr`, and `ping -t`. TCP probes, `traceroute -T`, do not get a Time Exceeded, because the helper's TCP socket does not have a TTL per segment.

### DNS on Linux

* The mode default is `split` when the manifest has domains, otherwise `none`. It is overridden by `defaults.dns` in the config and by the `--dns` flag.
* systemd-resolved is available when `resolvectl status` exits 0.
* `split`: `resolvectl dns tj0 <servers>`, `resolvectl domain tj0 ~d1 ~d2`, `resolvectl default-route tj0 false`.
* `all`: `resolvectl dns tj0 <servers>`, `resolvectl domain tj0 ~. <search domains>`, `resolvectl default-route tj0 true`.
* Revert: `resolvectl revert tj0`.
* Without resolved, in the `all` mode, the session writes `/etc/resolv.conf` with the servers and the search domains. When `/etc/resolv.conf` is a symlink, the session records the target in `/run/tj/resolv.conf.link` and recreates the symlink on revert. Otherwise it copies the file to `/run/tj/resolv.conf.backup` and copies it back on revert. `split` without resolved is an error.
* The servers default to the discovered resolvers of the remote, without the MagicDNS addresses `100.100.100.100` and `fd7a:115c:a1e0::53`.
* `split` needs the manifest `dns.domains`. Without them, `split` is an error with the name of the missing key.

### Session networks

The session networks are the result of a pure function in `internal/manifest`, which uses `go4.org/netipx` for the prefix arithmetic:

```
networks = manifest.networks
         + discovery.link_routes     (when discovery.link_routes is on and not --no-discovery)
         + discovery.cloud.networks  (when discovery.cloud_metadata is on and not --no-discovery)
minus manifest.exclude
minus 100.64.0.0/10 and fd7a:115c:a1e0::/48
minus the remote's tailnet addresses
minus the client's connected subnets
minus config.exclude and every --exclude
minus 0.0.0.0/8, 127.0.0.0/8, 169.254.0.0/16, 224.0.0.0/3, ::1/128, fe80::/10, ff00::/8
```

The result is the minimal sorted prefix list. `internal/cli`'s `sessionNetworks` gathers these inputs from the manifest, the discovery result, the local config, the resolved remote, and the `--network` and `--exclude` flags. It then calls `ComputeNetworks`. `connect`, `describe`, and `doctor` share it, so they report the same session networks for the same remote. A nil discovery result means that discovery did not run, or that its networks were set aside because of `--no-discovery`.

`describe` prints the manifest, the discovery result, every exclusion, the final list, the DNS mode, the DNS servers, the DNS domains, and the transport. It resolves the DNS mode and the transport the way `connect` does, without a flag override. `describe` does not fail on a DNS resolution error, for example a split mode without the manifest `dns.domains`. `describe` prints the error text as the DNS mode value. `describe` is the tool that finds this problem.

### Discovery script output

One JSON document on stdout:

```json
{
  "version": "1",
  "hostname": "gw.example",
  "uname_m": "aarch64",
  "exec_dir": "/root/.cache/tj",
  "manifest_path": "/etc/tj/manifest.yaml",
  "manifest": "<base64 of the manifest file>",
  "addresses": ["10.0.0.10/16", "2001:db8:0:1::a/128"],
  "link_routes": ["10.0.0.0/20", "2001:db8:0:0::/64"],
  "resolvers": ["10.0.0.2"],
  "search_domains": ["eu-west-1.compute.internal"],
  "cloud": {"provider": "aws", "networks": ["10.0.0.0/16", "100.64.0.0/16", "2001:db8::/56"]}
}
```

* `exec_dir` is the first of `$XDG_RUNTIME_DIR` and `$HOME/.cache/tj` where the script can create a file and `chmod` it. That probe is a check of write access only, because a write and a `chmod` both succeed on a `noexec` mount. This was verified on 2026-09-11. A `sh` script cannot prove execute permission, because a `sh` script still runs on a `noexec` mount. The exec of the uploaded helper is the execute check. The value is empty when neither directory is writable. A `memfd` loader runs a binary on a `noexec`-only remote. It needs a delivery path other than `sh`, so it is out of scope for v1 and recorded as a known option.
* `manifest_path` and `manifest` are absent when the remote does not have a manifest.
* The script reads `resolvers` and `search_domains` from `/run/systemd/resolve/resolv.conf` when that file exists, else from `/etc/resolv.conf`. The script drops `100.100.100.100` and `fd7a:115c:a1e0::53`. These facts were verified on the shared gateway on 2026-09-11. `/etc/resolv.conf` is in resolved's "foreign" mode with `100.100.100.100`. `/run/systemd/resolve/resolv.conf` contains `10.0.0.2` with `eu-west-1.compute.internal`.
* `cloud` is absent when a metadata service does not answer within 1 s. For AWS, the script uses IMDSv2 with a token. The script needs `curl`. Without `curl`, the script does not report `cloud`.
* The script reads the IPv6 VPC CIDR from IMDS, not from the connected routes. `ip -6 route show scope link` was empty on the gateway on 2026-09-11, while IMDS listed `2001:db8::/56`. The script reads routes with `ip -j route` for JSON. `base64 -w0` exists on the gateway.
* The script is POSIX `sh` and uses `ip`, `cat`, `base64`, `uname`, `mkdir`, `chmod`, `rm`, `rmdir`, and `curl` only. It prints the JSON itself. Every value is a CIDR, an address, a hostname, a path, or base64, so a JSON escape is not needed. The client parses the JSON strictly and validates every address and CIDR.

### SSH client

* Dial the remote's IPv4 tailnet address on port 22 with a 15 s timeout.
* Auth: the client tries `none` first. Tailscale SSH accepts `none` on the shared gateway, so the client authenticates with a nil `Auth`. This was verified on 2026-09-11. When a server refuses `none`, the client adds the ssh-agent keys at `$SSH_AUTH_SOCK` and a keyboard-interactive handler, because a check-mode server can challenge on either.
* `BannerCallback` prints the banner to stderr. In the normal accept case, the server sends an empty banner. The SSH channel that contains the check-mode sign-in URL is not verified, because check mode is off on this tailnet. The client prints the banner and any keyboard-interactive prompt, so the user gets the URL on the channel that contains it.
* `HostKeyCallback` uses `CacheDir/known_hosts`, keyed by the remote's hostname. A new key is stored. A changed key is a warning on stderr. The connection continues. The gateway key type is `ecdsa-sha2-nistp256`, stable across connections.
* The client sends `keepalive@openssh.com` every 30 s. The server answers with `ok=false` and a nil error. That is a completed round trip and a valid liveness signal.
* Tailscale SSH runs a command as `/bin/bash -c`, not a login shell, so `XDG_RUNTIME_DIR` is absent and `HOME` is `/root`. A non-zero exit propagates as `*ssh.ExitError` with `ExitStatus()`. Read a command's output with an explicit check. Under rapid session churn an exec channel returned empty stdout with a nil error once in the spike.

### Remote resolution

`tj connect <remote>` and `tj describe <remote>` accept an alias from the config, a hostname, or a tag such as `tag:example`. The client considers only online peers in the resolution, because the gateways are ephemeral and an old node deregisters on termination.

A tag matches every online peer that has it. A hostname matches an online peer whose HostName equals the reference. It also matches an online peer whose HostName equals the reference followed by a Tailscale collision suffix. That suffix is a hyphen and a number, for example `gw.example-1`. Tailscale adds that suffix when a replacement node is added to the tailnet before the old node with the same name deregisters, for example after an AMI replacement.

When more than one online peer matches, the client does not error. It picks the newest peer. An exact base-name match takes precedence over a suffixed match. Among suffixed matches, the match with the highest number takes precedence. For a tie that is left, the client picks the peer with the most recent WireGuard handshake. The client uses the same rule among tag matches. When the reference does not match an online peer, the client errors and lists the offline peers that the reference matches. A tag is the most robust reference across an AMI replacement, because the replacement has the same tag.

On a reconnect attempt, the unit resolves the same reference again with this algorithm, so the unit follows the remote across a move. The attempt ends without a dial on a status error, on a self node that is not online, or when the reference does not match an online peer. The self status is tailscaled's in-map-poll flag, false within seconds of a link-down. A resolved address that differs from the current one is a move: the unit logs `remote moved from <old> to <new>` and writes the new address to the state file at once, before it dials.

### Config file

The config file is `$XDG_CONFIG_HOME/tj/config.yaml`, with schema version 1:

```yaml
version: 1
defaults:
  user: root
  dns: split
  protocols: tcp,udp,icmp
  reconnect_for: 10m
exclude:
  - 192.168.0.0/16
remotes:
  gw:
    host: gw.example
    user: root
    dns: all
    protocols: tcp,udp
    networks:
      - 10.0.0.0/8
    exclude:
      - 10.1.0.0/24
```

The SSH user defaults to the local username. The precedence is the flag, then `remotes.<name>`, then `defaults`. A remote's host is the base hostname. The client also matches the host with a Tailscale collision suffix. Prefer a tag for a gateway that is recreated on an AMI replacement.

The same precedence applies to `reconnect_for`: `--reconnect-for`, then `remotes.<name>.reconnect_for`, then `defaults.reconnect_for`, then 10 minutes. With `--reconnect-for 0`, the reconnect is off.

A remote's `networks` and `exclude` are inputs of the session network computation. The entries of `networks` are added to the routed set, together with the manifest, discovery, and the `--network` flags. The entries of `exclude` are removed from the routed set, together with `config.exclude`, the manifest exclude, and the `--exclude` flags. See "Session networks". `tj config` writes the defaults and the global exclude. `tj alias` writes the aliases.

`tj config set exclude <cidr>[,<cidr>...]` replaces the global exclude list. `tj config unset <key>` clears one key: `defaults.user`, `defaults.dns`, `defaults.transport`, `defaults.protocols`, `defaults.reconnect_for`, or `exclude`. An unknown key is a usage error, with exit code 2. The error message contains the valid keys.

`tj alias unset <alias> <field>...` clears one or more fields of an alias: `user`, `dns`, `transport`, `protocols`, `networks`, `exclude`, or `reconnect_for`. An alias needs `host`, so `unset` does not clear it. An unknown field, or `host`, is a usage error, with exit code 2. The error message contains the valid fields. With the `--network` and `--exclude` flags, `tj alias set` replaces the whole list. It does not add to the list.

`config.Load` decodes the file with unknown fields rejected. It then validates the file:

* `version` is absent or 1.
* Each DNS, transport, and protocols value, in `defaults` and in every remote, is valid or empty.
* Each `reconnect_for` value, in `defaults` and in every remote, is empty or a Go duration of zero or more, for example `10m`.
* Each CIDR in `exclude`, in every remote's `networks`, and in every remote's `exclude`, parses.
* Every remote has a `host`.

The error contains the file path and the key, for example `remotes.gw.dns: invalid value "bogus", want none, split, or all`. Every command that loads the config reports a broken file at once, with exit code 1. The exception is `tj list`, which turns the error into a warning and continues.

On a write, `tj config` and `tj alias` replace the whole file. They thus drop the comments of a file that someone edited by hand. Edit the file directly to keep the comments. Run `$EDITOR $(tj config path)` to open it. The next command that loads the file validates it.

### CLI conventions

* `tj --help` groups the visible commands: Session commands `connect`, `disconnect`, `status`, `logs`; Inspection commands `list`, `describe`, `doctor`, `bench`; Configuration commands `alias`, `config`, `setup`. `completion`, `help`, and `version` are under Additional Commands.
* tj writes human output through `text/tabwriter`. `list`, `describe`, `status`, `doctor`, `bench`, and `connect --dry-run` accept `--json`.
* Errors are one line on stderr, `Error: <message>`. The exit code:

| Code | Meaning |
| ---- | ------- |
| 0 | Success. `tj status` without an active session also exits 0. `tj connect` to the remote of the active session, without `--replace`, also exits 0. |
| 1 | Runtime error: an error that the `RunE` of a command returns. `tj doctor` exits 1 when a check fails. |
| 2 | Usage error: an unknown command, a wrong number of arguments, an unknown flag, or an invalid flag value. |
| 3 | `tj connect` finds an active session to a different remote, and `--replace` is not set. |
| 130 | SIGINT stopped the command. |
* tj logs with `log/slog` and a text handler on stderr. With `-v`, tj logs at the debug level. The session unit logs to the journal through stderr.
* `_remote` and `_session` are hidden commands.
* `tj logs` prints the session log with `-n/--lines` (default 100, 0 for all) and `-f/--follow`. It works without an active session. It then shows the log of the last session, which is the main use after a failed connect. The hidden `_session logs` adds `--since` (RFC 3339), for the stream and the failure tail of a connect.
* `tj status` adds `RTT:` and `Traffic:` below `Path:` while the session has metrics. `RTT:` is the round-trip time of the transport and the helper, under the load of the session. `Path:` is the latency of the WireGuard path to the remote alone. It needs a disco ping only while the status is up. This is the rule of `tj list --path`. With `--json`, `tj status` adds `metrics` and `path.latency_ms` the same way. A state file from an older root copy does not have metrics. These rows and JSON keys thus stay absent until `tj setup` installs the new root copy and a new session starts.
* `tj doctor` runs the client checks: `tj version`, `tailnet`, `root copy`, `systemd-run` and `/dev/net/tun` on Linux, and `systemd-resolved`. `tj doctor <remote>` adds the remote checks: peer, SSH ok, discovery ok, manifest path or absent, exec dir, helper arch, parse manifest, session networks non-empty, session networks, DNS default mode, the QUIC transport, and the echo socket of the remote: `raw socket`, `ping socket`, or `none`, with the `ping_group_range` value for the last two. Each row has a status of `ok`, `fail`, or `info`. On a `fail` row, `tj doctor` exits with code 1. `tj doctor` does not run the manifest `checks`. `tj describe` only prints them.
* `tj connect` to the active session, without `--replace`, prints `session to <remote> already up (<uptime>); use --replace to restart` on stdout and exits 0. The check matches the address of the plan, or the reference of the plan while the status is `reconnecting`, because a move leaves the old address in the state file until an attempt resolves the new one. `tj connect` to a reconnecting session then prints `session to <remote> is reconnecting (attempt <n>); use --replace to restart` and exits 0. An active unit without a state file exits 3. `--replace` ends the session and starts a new one.
* `tj connect --reconnect-for <duration>` sets the reconnect window for one connect. See "Config file" for the precedence and the validation.
* `tj connect --dry-run` runs every step of the connect flow through the plan, prints it, and exits 0. It does not print a header line. It does not check for an active session and does not call sudo. The human layout matches `describe`: Remote, User, Transport, QUIC ports, Protocols, Reconnect for, DNS mode, DNS servers, DNS domains, Helper arch, Networks. With `--dry-run --json`, `tj connect` prints the plan JSON, the same bytes that `_session start` reads from stdin. `--json` without `--dry-run` is a usage error. The check runs before the remote resolution, so it does not need network access.
* Timeouts: SSH dial 15 s, helper upload 30 s, helper handshake 10 s, and 60 s for the wait of `_session start` on status `up`. Discovery does not have a limit of its own. The 5 s limit of `tj list --probe` applies to the TCP dial only.
* `tj list --path` sends up to 3 disco pings per remote through the local API, 200 ms apart, within 2 s, and stops at the first direct pong. A ping can time out while the remote moves data at the link rate: in spike 9, there were 4 timeouts of 3 s during downloads at 550 Mbit/s, while the session was fine at the same moments. The flap detector of the session reads the status endpoint every 10 s and does not send a ping, so the timeouts do not affect it.
* `tj list --probe` opens SSH to at most 8 peers at a time, and prints them in the tailnet status order regardless of which probe finishes first. It does not cache a probe result. It opens SSH again on each run.
* `tj bench <remote>` measures the throughput of the transport to the remote: the duration up, then the duration down, between the client and a temporary helper. It does not need a session or root. It does not change an active session. It follows `tj doctor <remote>`: it resolves the remote, dials SSH, runs discovery, and starts the temporary helper, then it measures. `--transport auto|quic|ssh` has the precedence of `connect`. With `auto`, `tj bench` falls back to the SSH transport with a reason. With `quic`, `tj bench` fails when the QUIC transport does not come up. The congestion controller is BBR in both directions. The manifest `transport.bandwidth` is ignored on purpose, because with Brutal the result is the configured rate, not the rate of the path. `--duration` is the time of each direction, 1s to 30s in whole seconds, default 5s. Any other value is a usage error, with exit code 2. The command has a time limit of 60s plus twice the duration plus 30s per direction. After that limit, or on an interrupt, it closes the SSH connection, so a remote that does not answer cannot hang the command. The two rates are the values for `transport.bandwidth` in the remote's manifest. An active session to the same remote shares the path and lowers the result.

## Testing

* Unit tests are next to the code. The `fake` platform serves `session` and `dns`.
* The loopback test in `internal/dataplane` runs the netstack, the mux client, and the helper in one process over `net.Pipe()`. It sends TCP to a local listener and UDP to a local echo server. It feeds an echo request for 127.0.0.1 and for ::1 to the capture and checks the reply packet. It also checks that the netstack answers a datagram with a port unreachable for a set without udp, and a SYN with a reset for a set without tcp. It does not need a device or root. The echo tests need the ping socket, so they skip with the `ping_group_range` value when the runner does not have the ping socket or `CAP_NET_RAW`.
* The mux loopback test in `internal/mux` pings 127.0.0.1 and ::1 through the helper in one process. It also sends a datagram to a closed loopback port. The kernel answers with a port unreachable, which comes back as an error frame while the flow stays open.
* The QUIC loopback test in `internal/mux` negotiates the transport over a yamux loopback and dials a helper listener on 127.0.0.1 in one process. It checks a TCP echo, a UDP echo, a refused second connection, a rejected wrong fingerprint on each side, a refused control stream, and a freed port after `quic-abandon` and after `quit`.
* The lane test in `internal/session` serves two helpers in one process over `net.Pipe()`, as the tcp lane and the udp lane of a lane dialer. It checks that the TCP echo and the UDP echo each went through the lane of its protocol. The routing tests next to it use recording dialers and cover the protocol lanes, the dns lane over UDP and over TCP, a port 53 flow to another address, and the fallback to the primary lane.
* The bench test in `internal/mux` runs each direction over a yamux loopback and over a QUIC loopback. It checks that the run moves bytes, that the clock covers the full duration, and that the rate is non-zero. It checks that the client refuses an invalid duration before it sends a request. It checks that the helper's own check refuses an invalid direction and an invalid duration with the other status. It also checks that a cancelled context ends a 30 s run at once and does not leave a goroutine behind.
* `test/e2e` is a rootless podman rig. The rig is `ubuntu:26.04` with systemd, systemd-resolved, and nftables. It runs with `--systemd=always --device /dev/net/tun --cap-add NET_ADMIN --cap-add NET_RAW --cap-add SYS_ADMIN`. The tailscaled socket is mounted at `/var/run/tailscale/tailscaled.sock`. The built `tj` is also mounted. `test/e2e/run.sh` runs it. The rig connects to `TJ_TEST_REF` as `TJ_TEST_USER`, default `root`. It checks that the session comes up on the QUIC transport with the helper port bound to the tailnet address only. It checks that the routes are in table 117 behind the two rules and not in main, and that the rules and the table are removed after `disconnect`. It checks TCP over IPv4 and IPv6 to the gateway's VPC addresses, and UDP DNS to the VPC resolver. It checks `ping` over IPv4 and IPv6 to the gateway's VPC addresses, because the VPC resolver does not answer an echo request. It checks `traceroute` in the UDP mode and in the ICMP mode to the gateway's VPC address, which is one hop from the helper. It checks that a session with `--protocols tcp,udp` does not get an echo reply while TCP and DNS still work. It checks that a session with `--protocols tcp` refuses `--dns all` and comes up with `--dns none`. It also checks the echo socket line of `tj doctor`, each DNS mode, `disconnect`, the one-session lock, and the empty remote. On the SSH transport, it checks the lanes. It checks the Transport line of `tj status` with the lane list. It checks the helper process count on the remote: 1 with `--protocols tcp`, 3 with `--dns none`, and 4 on the fallback session with `--dns split`. It checks the private name and `ping` through the fallback session, and the empty cache directory on the remote while a session is up. After `disconnect`, it checks for zero helper processes, files, and listeners. The count pattern is anchored to the command path, because the Tailscale SSH incubator quotes the exec command on its own command line. With `TJ_TEST_TRACE_TARGET` set to an address outside the VPC, it routes that address through the session. It checks that `traceroute` reports a Time Exceeded from at least one hop on the way. It then drops the helper's replies from the UDP range with an nftables input rule inside the rig. This drop has the same effect as a tailnet policy without the UDP rule. It checks the fallback to the SSH transport within 5 s plus the SSH baseline, with the warning in the journal. It also checks that `--transport quic` fails without a session. With an output-side drop, the send fails at once with EPERM, and the timeout path does not run. With `TJ_TEST_DERP_REF` set it also connects once to a DERP-relayed remote and checks the QUIC transport there. Without `CAP_SYS_ADMIN`, resolved fails to start. The rig's user-mode NAT does not keep the DF bit. A packet size check must thus run from a client that owns its own route, not from the rig. See spike 5.
* `test/e2e/crash.sh` runs in the same rig and sends SIGKILL to the session process. It then checks that a device, a route, a rule, a DNS change, or a remote file does not remain after the `ExecStopPost` cleanup. In scenario C, the script does the same on the SSH transport with three lanes. It also checks that a helper process does not remain on the remote.
* `test/e2e/bench.sh` runs in its own rig. It checks `tj bench` on the QUIC transport and on the SSH transport, its `--json` form, and its `--duration` range. It checks the fallback of `tj bench` to the SSH transport when the QUIC port range is blocked. It checks a run beside an active session: the session stays up with the same unit invocation and without a reconnect. It checks an interrupt that exits 130. It also checks that a file, a process, or a listener does not remain on the remote after every run and every disconnect. With `TJ_TEST_DERP_REF` set it also runs once against the relayed remote.
* `test/e2e/reconnect.sh` runs in the same rig and proves that a session rebuilds its transport after a loss. It runs six scenarios on both transports. The first three are a helper kill, a 30 s drop of every packet from the remote with `--dns all`, and a helper kill with the reconnect off. The other three are a disconnect during a reconnect, a drop that is longer than a 20 s window, and a gateway that is removed from the tailnet through a transient unit on that gateway. These times were measured on 2026-09-14 in the rig. After a helper kill on the QUIC transport, the session reaches `reconnecting` in 0.4 s and `up` in 0.5 s. On the SSH transport, these times are 0.5 s and 0.8 s. With a 30 s drop on the QUIC transport, the session reaches `reconnecting` in 14 s, and `up` 3.8 s after the drop ends. On the SSH transport, these times are 13.7 s and 2.1 s. `tj disconnect` during a reconnect returns in 0.2 s. With a 20 s window, the unit gives up 35 s after the drop starts. The move scenario ended on `remote changed` in 32 s, because every tagged gateway of the tailnet serves its own manifest.
* `test/e2e/metrics.sh` runs in its own rig, with `task e2e:metrics`. It checks the live metrics of `tj status` on both transports. It checks the `RTT:` and `Traffic:` rows and the latency in the `Path:` row within 5 s after the connect. It checks rates above zero during a `ping` burst to the gateway's VPC address, and totals that grow by the size of that burst. It checks a new `updated_at` within 7 s, the mode `644` of the state file, and that a leftover temp file does not exist. It checks 50 reads of `tj status --json` in a row that all parse. After `disconnect`, it checks the empty runtime directory and the empty remote. It then kills the helper on the QUIC transport and checks that the totals do not go down and that the `RTT:` row comes back after the reconnect. With `TJ_TEST_DERP_REF` set it also reads the rows once on the relayed remote. It does not write to `/etc/tj` on the remote, so a gateway with a manifest can serve as its target. These times were measured on 2026-09-17 in the rig, from the `session reconnected after` line of the session log. After a helper kill on the QUIC transport, the session with the sampler was up again in 1 s in 28 of 32 rounds, and in 2 s to 6 s in the other 4. A build without the sampler was up again in 1 s in 11 of 20 rounds, and in 2 s to 4 s in the other 9. The sampler thus does not add a delay to a reconnect.
* `test/e2e/loss.sh` is the loss job, `task e2e:loss`. It starts the bench server from `test/e2e/loss/bench` on the remote's VPC address and connects from the rig on each transport. It then adds `tc netem` loss and delay on the rig's own interface in both directions: egress on the interface, and ingress through an `ifb` device. The host and the remote thus do not have shaping. The script adds the shaping after the session is up, so the numbers are for the data plane and not for the SSH bootstrap. It measures 20 TCP connects through the session. It measures 20 UDP DNS queries with a 2 s limit, and the p50 and the p95 of their query time. It measures a bulk download as the median of 3 runs, with a 60 s time box per run. It also reads the `Reconnects:` row of `tj status` and counts the `session lost` lines in the journal. Its RESULT line contains `reconnects=<n> lost=<m>` per transport. At 7% loss and 30 ms delay with the 20 s yamux write timeout, three 10 minute runs had zero losses, two on the QUIC transport and one on the SSH transport. With the 10 s write timeout the QUIC transport had one yamux keepalive timeout in two 10 minute runs. At 30% loss both transports lose the mux within 30 s to 100 s. The knobs are `TJ_TEST_LOSS_PCT`, `TJ_TEST_DELAY_MS`, `TJ_TEST_TRANSPORTS`, `TJ_TEST_BULK_MIB`, and `TJ_TEST_TARGET=derp` for the relayed remote of `TJ_TEST_DERP_*`. The results are in `docs/spikes/06-loss-test.md`.
* `test/e2e/compare.sh` is the tj against sshuttle comparison of spike 9, `task e2e:compare`. It runs on the client, not in the rig, because the real direct path is not available in the rig. It runs one tunnel at a time: tj on QUIC, tj on the SSH transport, and sshuttle with `--dns` and the networks of `TJ_TEST_SSHUTTLE_NETS`. Per session, it measures a time-boxed download with probes every 5 s. The probes are a DNS query over UDP and over TCP, an ICMP echo, and a disco ping. The probes also include a TCP connect through the session, and a TCP connect to the remote's tailnet SSH port that does not go through the session. It then measures 20 TCP connects, 10 DNS queries, and the system resolver. It reads the remote from `test/e2e/target.env` or from the file in `TJ_TEST_ENV`. The knobs are `TJ_TEST_SESSIONS`, `TJ_TEST_CYCLES`, `TJ_TEST_DL_SECONDS`, `TJ_TEST_SSHUTTLE_NETS`, and `TJ_TEST_SSHUTTLE_BUF`. `TJ_SSH_LANES=1` in its environment is passed to `tj connect`. sshuttle needs a NOPASSWD sudo rule for its firewall helper. The results are in `docs/spikes/09-tj-against-sshuttle.md` and `docs/spikes/10-ssh-lanes.md`.
* `test/e2e/datagram` is the tool of spike 7. It runs a QUIC server and client in one process over 127.0.0.1 and compares the stream flow protocol with QUIC datagrams, shaped with `tc netem` on `lo`. It is measurement code and is not a part of tj. `docs/spikes/07-udp-datagrams.md` contains the result and the decision to keep the stream path.
* `TJ_QUIC_CONTROLLER=cubic` is a measurement knob on `tj connect`. It puts `cubic` in the plan and in the control line, and both sides then leave the library's Cubic in place instead of BBR or Brutal. It exists for the controller comparison of the loss job and has no other use.
* `TJ_SSH_LANES=1` is a measurement knob on `tj connect`. It puts `single_lane` in the plan, and the session then keeps the SSH transport on the primary lane, so a run compares the transport with the lanes and without them. It has no other use.
* `tj connect` never runs on the client machine during development. The client has the user's own tj session, and the one-session rule applies. The rig is the place for every connect test. The exception is `test/e2e/compare.sh`, which the user starts on the client on purpose.

## Performance

The measurements are in the spikes:

* `docs/spikes/01-tun-netstack.md`: the first data plane against sshuttle.
* `docs/spikes/05-quic-transport.md`: the QUIC transport.
* `docs/spikes/06-loss-test.md`: the loss job and the tuning decisions.
* `docs/spikes/09-tj-against-sshuttle.md` and `docs/spikes/10-ssh-lanes.md`: the comparison on a client.
