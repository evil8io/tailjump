# Architecture

This document has the decisions that `docs/spec.md` leaves open. A chunk follows this document. A change to a decision here is a PR on this document first.

## Module and layout

Module `github.com/evil8io/tailjump`, Go 1.27, CGO off on every target.

## Deviations from the spec

1. **No `tailscale.com` dependency.** The spec names `tailscale.com/client/local`. That module pulls the whole Tailscale tree, including a pinned gVisor and a wireguard-go fork. The data plane needs its own gVisor version, and two pinned gVisor versions in one build is a conflict. `tj` reads the local API over the unix socket with `net/http` instead. The socket is `/var/run/tailscale/tailscaled.sock` on Linux and `/Library/Tailscale/ipnport` plus a port file on macOS; the tailnet package hides the difference. The request is `GET http://local-tailscaled.sock/localapi/v0/status` with the header `Sec-Tailscale: localapi`. A raw read returned the peers on 2026-09-11.
2. **TUN device from `golang.zx2c4.com/wireguard/tun`.** The data plane wraps `tun.Device` in a gVisor link endpoint. This is the only wireguard-go package in the build.


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
| `test/e2e` | the container rig against the shared gateway |

## Build

1. `task helpers` builds `cmd/tjhelper` for `linux/amd64` and `linux/arm64` with `-trimpath -ldflags "-s -w"` into `internal/helper/embed/bin/tj-helper-linux-<arch>`.
2. `task build` runs `task helpers`, then builds `cmd/tj` for the host into `bin/tj`.
3. `internal/helper/embed` embeds `all:bin`. The directory has a `.gitkeep`, and the binaries are in `.gitignore`. A missing helper binary is a runtime error at `connect`, not a build error, so `go build ./...`, `go vet ./...`, and `go test ./...` work without `task helpers`.
4. goreleaser runs `task helpers` in `before.hooks` and builds `linux/amd64`, `linux/arm64`, and `darwin/arm64`. The version comes from the git tag through `-X github.com/evil8io/tailjump/internal/version.Version=`.
5. `cmd/tjhelper` imports no package that imports `tailscale.com`, cobra, gvisor, or `log/slog`. It imports yamux, the quic-go fork `github.com/apernet/quic-go`, `crypto/tls`, `golang.org/x/sys/unix` for the socket error queue, and `internal/congestion`. `encoding/json` enters the helper graph through the fork's TLS fingerprint feature, which tj does not use; the mux package itself still builds its lines with `fmt.Appendf` and parses them with `strconv`. Measured 2026-09-12 with `-trimpath -ldflags "-s -w"`: 6.50 MiB for linux/arm64 and 7.03 MiB for linux/amd64 with the echo flow, against 6.38 MiB and 6.93 MiB with the transport alone, and 2.25 MiB and 2.30 MiB before the transport. The gate is 8 MiB, and `task helpers` fails at or above it. There is no room for a second large dependency.
6. The fork publishes no usable module tag: its semver tags declare the upstream module path. The pin is a pseudo-version of its `v0.61.0-mod-rename` branch, and Renovate cannot follow it by tag. The reason for the fork is `Conn.SetCongestionControl`, which upstream does not export; see `docs/spikes/05-quic-transport.md`.

## Platform interfaces

`internal/platform/platform.go` has one interface per concern. The Linux implementation is in `internal/platform/linux`, the macOS one in `internal/platform/darwin`, and the in-memory one in `internal/platform/fake`. Build tags select the real implementation, and `platform.New()` returns it. A chunk may extend an interface. The fake then follows in the same PR. `linux` and `darwin` cannot import this package, because this package already imports them; a method whose interface signature names a type from here, for example `Logs` and `LogOptions`, reaches `linux.Runner` and `darwin.Runner` as plain arguments instead, and `platform_linux.go` and `platform_darwin.go` each wrap the concrete Runner to restore the interface shape.

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

1. Upload: `sh -c 'd="${XDG_RUNTIME_DIR:-$HOME/.cache/tj}"; mkdir -p "$d" && f="$d/tj-helper.$$" && cat > "$f" && chmod 0700 "$f" && echo "$f"'`. The client writes the helper for the remote's architecture to stdin, closes stdin, and reads the path from stdout.
2. Run: `exec <path> _remote`. Stdin and stdout are the mux transport. Stderr is the helper log. The client writes it to its own log.

On the SSH transport the session opens one more SSH connection per extra lane, each with its own helper process and its own yamux session over that helper's stdin and stdout. A lane is one protocol of the set, `tcp`, `udp`, or `icmp`, or the session's DNS servers. The primary lane is the connection of item 2 above: it carries the control stream, the QUIC negotiation, and the `quit`, `icmp`, and `unlink` verbs, and it is the lane of the first protocol of the set in the order `tcp`, `udp`, `icmp`. A session has at most four lanes, one per protocol and one for the DNS servers, and never a lane per flow. Every lane runs the same exec of the same file, because Linux allows the exec of a file that another process already runs, so the client uploads the helper once and makes no copy. The QUIC transport opens no extra lane.

A flow takes the lane of its protocol. A flow to one of the session's DNS servers on port 53, over UDP or over TCP, takes the dns lane, so a query does not wait behind a bulk flow of the same protocol. A protocol whose lane did not open takes the primary lane. The dns lane exists with `--dns split` and with `--dns all`; `--dns none` names no servers, so the session opens no dns lane and such a flow takes its protocol lane.

The helper keeps its own file at start. It removes the file when the client sends the control verb `unlink`, and at its exit. The client sends `unlink` on the primary control stream right after the transport selection: at once on the QUIC transport, and on the SSH transport after every extra lane's handshake has finished, whether it succeeded or failed. It waits for the answer and logs a warning when the remove failed. The file exists during the connect phase only. The helper ignores SIGPIPE, because the Go runtime otherwise ends a process on a write to a closed stdout without its deferred remove; with the signal ignored, the write fails, the mux closes, and the exit path removes the file. Measured on the shared gateway on 2026-09-12: Tailscale SSH kills the command's process tree at once when the client connection drops, and a helper that was idle at that moment still removed its file at exit. A SIGKILL of the helper and a power loss on the remote are the two paths that leave a file behind, so every helper removes at start each `tj-helper.*` file in its own directory whose modification time is older than 10 minutes, and never its own file. `tj doctor` starts a temporary helper and sends `quit`, and that helper removes its file at its exit, so the doctor sends no `unlink`. At exit, the helper also removes `$HOME/.cache/tj` when that directory is empty. Tailscale SSH sets no `XDG_RUNTIME_DIR`, verified on the shared gateway on 2026-09-11, so the cache path is the common case.

The architecture map is `x86_64` to `amd64` and `aarch64` to `arm64`. Any other value is the error "unsupported remote architecture".

### Mux protocol v5 on the SSH transport

The SSH transport is the helper's stdin (client to helper) and stdout (helper to client). Protocol v2 differs from v1 in the handshake line, the control verbs of the QUIC negotiation, and the probe stream kind. Protocol v3 differs from v2 in the UDP frame format, which carries the TTL and the ICMP errors, in the ICMP echo stream kind, and in the `icmp` control verb. Protocol v4 differs from v3 in the `unlink` control verb; the stream kinds and the flow frames do not change, and every lane's helper is the same binary. Protocol v5 differs from v4 in the bench stream kind; nothing else changes. The client uploads the helper embedded in its own binary, so the two always share one version, and the protocol number is a guard, not a negotiation.

1. Handshake: the helper writes the line `TJ5\n` to stdout at start. The client waits at most 10 s for that line. Any other line before it is an error message.
2. Then both sides run yamux over the transport: the client is `yamux.Client`, the helper is `yamux.Server`. Config: `EnableKeepAlive` on, `KeepAliveInterval` 5 s, `ConnectionWriteTimeout` 20 s, `MaxStreamWindowSize` 4 MiB. The keepalive is the liveness check of the session.
3. The client opens every stream. The first byte of a stream is its kind: `0` control, `1` TCP, `2` UDP, `3` probe, `4` ICMP echo, `5` bench. A probe stream gets the status byte `0` and a close; the QUIC transport uses it right after its handshake.
4. A TCP, UDP, or ICMP echo stream continues with the destination: 1 byte address length (4 or 16), the address bytes, and 2 bytes port, big-endian. On an echo stream the port field is the ICMP identifier. The helper answers with 1 byte status: `0` ok, `1` refused, `2` unreachable, `3` timeout, `4` other, `5` unsupported. On a status other than `0`, the helper closes the stream. On a TCP stream the client waits for the status byte before it sends data. On a UDP stream and on an echo stream the client does not wait; see the UDP item.
5. TCP: after the status byte, the stream is the connection. A `Close` on one side is a half-close. The other side reads EOF and can still write.
6. UDP: the client does not wait for the status byte. It sends the destination and then the first datagram frame at once, to save a round trip. A frame is 2 bytes length, big-endian, then the body; both sides send frames. A request frame body is 1 byte TTL, then the datagram; the helper sets the TTL on its socket before the send, so a traceroute probe keeps its TTL. A reply frame body starts with 1 byte kind: `0` a datagram, then the payload; `1` an ICMP error, then 1 byte type, 1 byte code, 4 bytes info (the MTU of a Packet Too Big), 1 byte address length, the address of the sender of the error, and the bytes of the original datagram that the error quoted. The helper has one UDP socket per stream, connected to the destination, with `IP_RECVERR` or `IPV6_RECVERR` on, and it reads the ICMP errors from the socket's error queue; the flow continues after an error. On a dial error it writes a non-zero status byte and closes, which the client treats as the end of the flow. Each side closes the stream after an idle time: 60 s by default, and 10 s when the destination port is 53. A stream close ends the flow. Measured 2026-09-11: the status wait cost one round trip, 61 ms against 31 ms for a DNS query on a 29 ms path.
7. ICMP echo: one stream per pinged address and identifier, so one `ping` process is one flow and its sequence numbers never mix with another's on the remote. The client sends the destination and the first request frame at once. A request frame body is 2 bytes sequence, 1 byte TTL, then the echo payload. The helper answers the status byte first: `0`, or `5` when it has no echo socket, after which it closes and the client drops every echo request of the session with one warning. A reply frame body starts with 1 byte kind: `0` an echo reply, then 2 bytes sequence, 4 bytes round-trip time in microseconds as the helper measured it, and the payload; `1` an ICMP error with the layout of the UDP item, where the quoted bytes start at the original ICMP header. The helper opens, in this order, a raw ICMP socket, which needs `CAP_NET_RAW`, then a ping socket, `SOCK_DGRAM` with `IPPROTO_ICMP` or `IPPROTO_ICMPV6`, which the sysctl `net.ipv4.ping_group_range` gates. On a raw socket the helper sets a random identifier, filters the replies and the errors by it, and parses the errors from the messages themselves; on a ping socket the kernel sets the identifier, delivers only the matching replies, and the helper reads the errors from the error queue. The helper sets the TTL per request. The idle time is 60 s on both sides.
8. Bench: one stream per direction, opened by `tj bench`. After the kind byte, the stream has 1 byte direction, `0` up or `1` down, and 2 bytes duration in seconds, big-endian, 1 to 30. Up means the client sends; down means the helper sends. The helper answers with 1 byte status: `0` ok, or `4` other for an invalid request. On `4` it closes the stream. On an up run the helper discards what it reads until EOF, with a read deadline of the duration plus 10 s. It then writes 8 bytes big-endian with the byte count it received. The client's clock runs until that count arrives, so the drain of the transport buffers counts as part of the measurement. On a down run the helper writes 32 KiB blocks of zeros for the duration, then closes. The client counts the bytes until EOF. The 30 s cap on the duration bounds the load a client can put on the remote. `tj bench` uses the same stream kind on the SSH transport and on the QUIC transport.
9. Control: exactly one control stream, opened first. The helper writes one JSON line: `{"version":"…","goos":"linux","goarch":"…","hostname":"…","pid":…}`. The client can write the line `quit\n`. The helper then closes its QUIC listener and exits. The helper also exits when the transport closes. The client can write the line `icmp\n`, and the helper answers `icmp raw|ping|none [<low>-<high>]\n` with the echo socket it can open and the `ping_group_range` value; `tj doctor` reports it. The client can write the line `unlink\n`, and the helper removes its own file and answers `unlink ok\n`, or `unlink <reason>\n` when the remove failed, so the client knows the file is gone before the session reports up. An extra lane's helper receives none of these verbs: the client uses an extra lane for flows, for the liveness wait, and for `quit` only. The other control verbs are the QUIC negotiation below.

The helper dials with a 10 s timeout from the remote's default source address. It opens no listener beyond the QUIC listener of the transport below.

### Transport v2: QUIC over the tailnet

Every TCP connection and UDP flow of a session runs as a stream of one QUIC connection between the client and the helper, over UDP to the remote's IPv4 tailnet address. Tailscale SSH stays the bootstrap, the only trust anchor, the control stream, and the fallback transport. The reason is the relayed path: a remote in a private subnet has no direct Tailscale path, DERP relays every packet, the relay loses packets, and one TCP connection that carries every flow collapses on that loss with head-of-line blocking and one shared congestion window. QUIC recovers per packet and keeps each stream independent. The spike that settled the numbers is `docs/spikes/05-quic-transport.md`, and the spec is K8S-206.

Negotiation on the control stream, after the info line:

1. The client writes `quic <bind-address> <first-port>-<last-port> <controller> <client-fingerprint>\n`. The bind address is the tailnet address the client dialed for SSH. The port range comes from the manifest `transport.quic_ports`, default `7443-7452`, because the helper cannot read the manifest. The controller is `bbr`, or `brutal=<bytes-per-second>` with the helper's send rate from the manifest `transport.bandwidth.down`. The fingerprint is `sha256:<hex>` of the client's DER certificate. This line is the chunk 2 amendment of K8S-206 contract C1, because the C1 line had no room for the range and the controller.
2. The helper generates a self-signed ECDSA P-256 certificate in memory, binds the first free UDP port of the range on the bind address only, never the wildcard address, starts the QUIC listener, and writes `quic <port> <helper-fingerprint>\n`. When no port of the range binds, it writes `quic-unavailable <reason>\n`, and the session uses the SSH transport.
3. The client dials `<bind-address>:<port>` with its certificate, ALPN `tj/5`, TLS 1.3, and a verify function that accepts only the helper certificate with the pinned fingerprint. The helper requires a client certificate and accepts only the pinned client fingerprint, and it refuses every handshake once a connection exists.
4. Right after the handshake the client opens a probe stream, kind `3`, and waits for the status byte. TLS 1.3 lets the client complete the handshake before the helper has verified the client certificate, so only an answered probe proves that the helper accepted the connection. The dial, the probe, and the answer share one 5 s budget.
5. When that budget passes without an answered probe, the client writes `quic-abandon\n`, the helper closes the listener, and the session uses the SSH transport with one warning that names the policy rule, `udp:<first>-<last>`.
6. The helper accepts one QUIC connection per session. Both certificates are in memory only; the helper writes no file.

QUIC configuration, on both sides: `InitialPacketSize` 1232 and `DisablePathMTUDiscovery` on, which are mandatory, because the tailnet path MTU is 1280 and the library default of 1280 bytes of payload completes no handshake in either direction, measured in spike 5; `MaxIdleTimeout` 15 s; `KeepAlivePeriod` 5 s; `MaxIncomingStreams` and `MaxIncomingUniStreams` 65536, because the library default of 100 is too low for a tunnel; 0-RTT off; datagrams off; no connection migration. The receive windows are 8 MiB per stream and 20 MiB per connection on both sides, initial and maximum, because spike 6 measured 22% to 55% more throughput under 7% loss than the library defaults through a tj session; spike 5 saw no gain on a raw stream. The UDP socket buffers are the library behaviour: the fork asks for 8 MiB with `SO_RCVBUFFORCE`, which both root processes get, measured in spike 6.

Congestion control: both sides install the controller right after the handshake with the fork's `Conn.SetCongestionControl`. BBR is the default. A manifest with `transport.bandwidth` selects Brutal: the client sends at `up`, the helper at `down`. Brutal sends at the configured rate and compensates loss, which is unfair on a shared relay, so it stays opt-in. Spike 5 measured 261.3 Mbit/s for BBR against 1.7 Mbit/s for Cubic at 7% loss and 30 ms delay.

Flows over QUIC: the client opens every stream, and the first byte is the kind, `1` TCP, `2` UDP, `3` probe, or `4` ICMP echo. Kind `0` is refused, because the control stream stays on the SSH transport. The destination, the status byte, the frames, and the idle timeouts are the same as on the SSH transport, and the helper dials the same way. A TCP flow's `Close` on a QUIC stream closes the send side only, and the peer reads EOF; a second `Close` at flow end releases the read side with `CancelRead`. `internal/dataplane` takes a `mux.Dialer` and does not know which transport serves it.

Session lifecycle: the transport is selected right after the mux is up and before the device exists, so `--transport quic` fails early. The session opens the extra lanes after that selection and only when it ends on the SSH transport, so a QUIC session pays nothing for them; the lanes open in parallel, each with the yamux keepalive of 5 s. An extra lane that does not open is one warning that names the lane and the reason, and its protocol then takes the primary lane; a lane does not reconnect. The yamux keepalive of the primary lane stays the liveness check of the session, and a yamux session that closes on any lane ends the session, by the path a closed primary mux takes. A QUIC connection close ends the session by that same path. A dead path is known within 15 s on the QUIC transport, and within 25 s on the SSH transport. The write timeout on the primary lane's yamux session is 20 s, not 10 s, because the loss job measured one keepalive timeout in 20 minutes at 7% loss on that connection while a QUIC session ran a saturated download with the old 10 s timeout, and the QUIC idle timer never fired in the same run. A nearly idle TCP connection under loss stalls through RTO backoff, and a busy lane does not. The stop path closes the QUIC connection, sends `quit` on every lane's control stream, and closes every lane's mux and SSH connection; the helper closes its listener before it exits, and it also closes the listener when the SSH transport closes. The state file records `transport`, `quic_port`, `fallback`, and `lanes`, and `tj status` prints `quic (port N)`, `ssh, lanes tcp,udp,icmp,dns`, or `ssh (fallback: <reason>), lanes tcp,udp,icmp,dns`. `tj doctor` brings the transport up through a temporary helper, reports `ok, port N` or the reason, and tears it down.

Selection: `tj connect --transport auto|quic|ssh`, then `remotes.<name>.transport`, then `defaults.transport`, default `auto`. `quic` fails the connect when the transport is unavailable, with no fallback; it is the test mode. `ssh` skips the negotiation.

Cost of the fallback, measured in spike 9 on a client on 2026-09-12 before the lanes: with a bulk download in the session, a DNS query took up to 795 ms on the SSH transport over the relayed path and up to 355 ms over the direct path, against 74 ms and 60 ms on QUIC, because the one TCP connection with the 4 MiB yamux window queued every flow behind the bulk flow. With the lanes, measured in spike 10 on the same client and remotes on the same day: a DNS query and an ICMP echo under load take at most 31 ms on the direct path, which is the idle round-trip time, and at most 147 ms on the relayed path, where a TCP connect next to the session takes up to 147 ms too, so the remaining latency there is the relay queue, which BBR on the QUIC transport keeps short and a lane cannot remove. The bulk rate and the idle latency do not change, and four lanes add at most one second to the connect. `docs/spikes/09-tj-against-sshuttle.md` and `docs/spikes/10-ssh-lanes.md` have the tables.

Security: the listener binds the tailnet address only, so it is not reachable from the VPC interface or from the internet. The tailnet policy gates the port range with one UDP rule per remote, for example `{"src": ["group:example"], "dst": ["tag:example"], "ip": ["udp:7443-7452"]}`; without it the session falls back. Mutual TLS pins both certificates by fingerprints that cross the already-authenticated SSH channel, so there is no second trust anchor. The helper accepts one connection per session. Keys and certificates are per session and in memory. The listener closes on `quic-abandon`, on `quit`, and when the SSH transport closes.

Invariants: the tailnet range is excluded from the session networks in every version, so the client's UDP socket to the tailnet address is never routed through `tj0`. The QUIC transport forwards exactly what the SSH transport forwards, the flows to the session networks, and adds no reachability. No file, listener, or process remains on the remote after a session, on every exit path.

## The session

### Privilege on Linux

`tj setup` copies the running binary to `/usr/local/libexec/tj/tj` (root, 0755) and writes `/etc/sudoers.d/tj` with `<user> ALL=(root) NOPASSWD: /usr/local/libexec/tj/tj`. On Linux it checks `systemd-run` and `/dev/net/tun`, then runs `sudo` interactively once, and validates the file with `visudo -c`. When `session.CheckRootCopy` already passes, `setup` prints `setup is current (<version>)` and returns without the tool checks and without sudo.

The root copy is root-owned, so the NOPASSWD rule does not point at a user-writable file. `tj connect` compares the output of `/usr/local/libexec/tj/tj version` with its own version, and refuses on a mismatch with the message to run `tj setup`.

### Connect flow

1. The unprivileged `tj connect` resolves the remote, opens SSH, reads the manifest, runs discovery, and computes the session networks. It refuses on an empty set. It refuses when a session to another address is active, unless `--replace`. A session to the address of the plan is the already-up case in "CLI conventions".
2. It writes the plan as JSON to the stdin of `sudo -n /usr/local/libexec/tj/tj _session start`. When the effective uid is 0, it runs the same code in-process without sudo.
3. `_session start` writes the plan to `/run/tj/plan.json` (0600) and starts the unit: `systemd-run --unit tj-session --collect --property KillMode=mixed --property TimeoutStopSec=20 --property "ExecStopPost=<root tj> _session cleanup" <root tj> _session run /run/tj/plan.json`. With `--foreground`, it runs `_session run` in-process instead.
4. `_session run` opens SSH, uploads and starts the helper, opens the mux, selects the transport, opens the extra lanes on the SSH transport, creates the device, adds the routes, applies the DNS mode, writes the state file with status `up`, and waits for a signal or a loss. On the final exit, a signal or a reconnect that gives up, it reverts the DNS, removes the routes and the device, closes the QUIC connection, sends `quit` to the helper of every lane, and removes the state file.
5. A loss is a mux close, a lane close, a QUIC connection close, or a failed resume probe. With `reconnect_for` 0 a loss ends the session, the way a signal does, and the unit logs `session ended: <reason>`, or `session ended: lane mux closed` with the lane name. With `reconnect_for` above 0 a loss starts a reconnect instead. The unit logs `session lost: <reason>`, clears the switch dialer, closes the transport, reverts the DNS, and writes status `reconnecting`. The switch dialer is the `mux.Dialer` in front of the data plane; it fails every new dial while no transport exists, so a new flow gets a reset or an unreachable answer at once, instead of a hang, and an open flow ends with its stream. The device, the routes, and the netstack stay. The unit closes the SSH connection before the mux: a `Close` on an SSH channel does not end a pending read, and a yamux `Close` waits for its receive loop.
6. Each attempt resolves the remote again and follows a move. See "Remote resolution". After a dial to a new address, and before the helper upload, the unit reads the manifest and compares its SHA-256 hash with the plan's `manifest_sha256`. A mismatch ends the unit with `remote changed`, and the user runs `tj connect` again. The reconnect keeps the transport of the session: a QUIC session that gets no QUIC on an attempt logs `quic transport unavailable: <reason>` as a failed attempt and retries, and an SSH session selects SSH without a negotiation.
7. The backoff starts at 1 s and doubles to a cap of 10 s. The window is `reconnect_for`, default 10 minutes. It starts at the loss, on the Go monotonic clock, which stops during a suspend, so the window counts awake time only. A successful reconnect logs `session reconnected after <duration> (attempt <n>)` and ends the window; a later loss starts a new reconnect with a full window. After the window passes, the unit logs `reconnect gave up after <duration>` and exits the way a loss without a reconnect window does.
8. A resume probe finds a loss that no close reports, because a client that suspends can lose its transport without a close on either side. A ticker reads the wall clock every second, and a gap above 5 s between two reads marks a suspend. One probe then follows, with a 3 s limit: a yamux ping on the primary lane, or a QUIC probe stream. A failed probe is a loss. The detector runs only when the reconnect window is above 0.
9. A stop signal wins over a reconnect at once: it cancels the backoff wait, the resolve call, and the dial. The `ExecStopPost` cleanup does not change. The reconnect code is platform-neutral; it builds on macOS, and its behaviour there is unverified, like the rest of the session runner.
10. `tj connect` prints `connecting to <hostname> (<addr>) as <user>` on stderr, right after the remote resolution. `_session start` then waits up to 60 s for the state file with status `up`, or for the unit to fail. It prints nothing on success. `tj connect` reads the state file after that wait and prints one line on stdout: `session to <remote> up: <transport>, dns <mode>, <n> networks, <elapsed>`. The elapsed time runs from the start of the command. With `-v` the root copy follows the session log on stderr during the wait, from the start of the unit. The steps of the unit then print as they happen. Without `-v` the header line and the final line are the whole output of a connect that succeeds. When the unit fails, or when the wait times out, the root copy prints the last 20 lines of this session's log on stderr. The error then ends with `see tj logs for the full log`. With `-v` there is no tail, because the stream already printed those lines. A signal during that wait cancels the connect. `tj connect` sends SIGINT to `sudo` instead of a kill signal, and sudo relays it to the root copy. The root copy stops the unit and waits up to 25 s until the unit is inactive. It then prints `connect cancelled; the session is stopped` and exits 130. sudo 1.9.14 and later run the root copy in a pseudo-terminal, so a Ctrl-C can reach that child alone. `tj connect` then reads the exit code 130 of sudo as the interrupt, and exits 130 itself. A signal after the session came up changes nothing: the session stays up.
11. `tj disconnect` runs `sudo -n /usr/local/libexec/tj/tj _session stop`, which runs `systemctl stop tj-session`. It reads the remote from the state file before the stop, and prints `session to <remote> ended` on stdout.
12. `_session cleanup` runs after every stop. It reverts the DNS on `tj0`, restores `/etc/resolv.conf` from the backup, deletes `tj0` when it exists, removes the session rules and flushes the session table, and removes the state and plan files. Every step is safe to repeat.

On Linux the session logs to the journal of the unit `tj-session`. On macOS it logs to `RuntimeDir/session.log` (0640), and `Start` opens that file with `O_TRUNC`, so it has the log of the last session only. `tj logs` prints it on both platforms through `Runner.Logs`; an unprivileged caller reaches it the way `tj disconnect` reaches `_session stop`, through `sudo -n <root copy> _session logs`.

The plan JSON: `{"remote":…,"addr":…,"user":…,"ref":…,"networks":[…],"dns":{"mode":…,"servers":[…],"domains":[…]},"helper_arch":…,"manifest_sha256":…,"transport":"auto|quic|ssh","quic_ports":"7443-7452","bandwidth_up":0,"bandwidth_down":0,"protocols":"tcp,udp,icmp","single_lane":false,"verbose":false,"reconnect_for":600}`. The bandwidths are bytes per second, zero for BBR. An empty `protocols` means all three. `single_lane` keeps the SSH transport on the primary lane, and `tj connect` sets it from `TJ_SSH_LANES`. `verbose` sets the log level of the session to debug, and `tj -v connect` sets it, because the unit inherits no flag and no environment. `ref` is the reference the unit resolves again on a reconnect, a hostname or a tag. `manifest_sha256` is the hex SHA-256 of the manifest bytes connect read; a reconnect after a move compares it to guard against a changed remote. `reconnect_for` is the reconnect window in seconds, 0 for off.

The state file `/run/tj/session.json` (0644): `{"remote":…,"addr":…,"user":…,"ref":…,"networks":[…],"dns":{…},"started_at":…,"pid":…,"status":"starting|up|reconnecting|stopping","transport":"quic|ssh","quic_port":…,"fallback":…,"protocols":"tcp,udp,icmp","lanes":"tcp,udp,icmp,dns","reconnect":{"since":…,"attempts":…,"reason":…},"reconnects":0}`. `lanes` are the SSH connections that opened, in the order tcp, udp, icmp, dns, and it is empty on the QUIC transport. `ref` is the reference the session resolves again on a reconnect. `reconnect` holds the progress of a loss while the status is `reconnecting`: the RFC 3339 time of the loss, the attempt count, and the reason for the loss or the last failed attempt. `reconnects` counts the losses the session recovered from, and it survives a reconnect. `tj status` reads the file without root and prints `Protocols:`, and above zero, `Reconnects:`.

### Session metrics

While the status is up, a sampler probes the current transport once a second. The probe is a yamux ping on the SSH transport, or one probe stream on the QUIC transport. A failed probe sets no loss. The mux keepalive and the resume detector already find a dead transport, so one lost probe under load is a normal event. The sampler sends its first sample right after the first probe, about a second after the session comes up. It then sends one sample every 5 s.

A sample has the mean round-trip time of the probes that answered in the interval, absent when none did. It also has the byte totals since the session start, and the rates of the interval in bytes per second. Up counts the packets the client read from the TUN device, and down counts the packets it wrote to the device.

The sampler owns no state of the runner: it sends each sample on a channel. The runner goroutine is the only writer of the state file, because that goroutine already writes the state on every other session event. `runner.wait` reads the sample from that channel and calls `writeState` before it waits again. A loss zeros the round-trip time and the rates, because the session moves no traffic and has no transport to probe while it reconnects. The totals stay, because the data plane stays, and they continue across the reconnect for the same reason.

The cost of the sampler is one probe a second on the transport. `writeState` now writes a temp file next to the state file and renames it over the state file. The reason is new: `tj status` and the wait of `tj connect` read the file while the session writes it every 5 s. A truncating write would hand a reader a partial document.

### Protocol set

`tj connect --protocols tcp,udp,icmp` selects the protocols the client forwards, then `remotes.<name>.protocols`, then `defaults.protocols`, default all three. The set is a comma-separated list of `tcp`, `udp`, and `icmp` in any order. The enforcement is on the client only: a disabled protocol gets no forwarder in the netstack, so the netstack answers TCP with a reset and UDP with a port unreachable, and the pump drops an ICMP echo request. The wire protocol and the helper do not change with the set. `--dns split` and `--dns all` send the queries through the tunnel, so `connect` refuses them when the set has no `udp` and names the conflict; `--dns none` works with every set. There is no manifest key: the remote's security group and the operator's policy on the remote gate what the helper reaches. `tj describe` prints the effective set from the config, and `tj doctor` prints the echo socket of the remote.

The lock is the unit name `tj-session.service` plus the state file. macOS uses the pid in the state file instead of a unit.

### Device and routes

The device is `tj0` with MTU 1500. Its addresses are `169.254.117.1/32`, `fd00:117::1/128`, and `fe80::1/64`. The global-scope ULA `fd00:117::1/128` is required as an IPv6 source. RFC 6724 rejects a link-local source for a global destination, so a client without global IPv6 has no source at all without it, measured 2026-09-11. The session adds one `dev tj0` route per session network, IPv4 and IPv6. The session also adds a host route for each DNS server that is outside the session networks, so the queries are captured.

On Linux the routes go into the routing table 117, behind the rule `pref 5300 lookup 117` for IPv4 and for IPv6, and not into the main table. tailscaled marks its own UDP packets with the bypass mark `0x80000`, and its rules 5210 to 5250 send a marked packet to the main table, then the default table, then to unreachable. A session route in main captures tailscaled's packets to an endpoint inside the session networks. The remote advertises its own VPC address as a WireGuard endpoint, so the discovery pings travel through the tunnel, magicsock switches to that path, the path carries its own transport and blackholes, and magicsock falls back to DERP; the cycle repeats every 15 to 20 s. Measured on a client on 2026-09-12: DNS answered 14 of 33 queries with the routes in main and 24 of 24 with the remote's address excluded; see `docs/spikes/08-routing-loop.md`. The rule is after tailscaled's rule 5270 and before main at 32766, so every unmarked packet still reaches the session routes, and the remote's own address stays reachable through the tunnel. `Router.Reset` flushes the table and deletes the rules on every exit path, because a device delete removes the routes but not the rules. On macOS the routes stay in the main table, because tailscaled binds its sockets to the default interface there.

The session samples tailscaled's endpoint for the remote every 10 s and logs one warning when the path changes three or more times within two minutes. A loop flaps every 15 to 20 s, and a direct-path discovery changes the path once. An endpoint inside the session networks is not a fault on its own: a remote with a global VPC IPv6 address has its real direct endpoint inside the routed /56, measured 2026-09-12. The rig cannot reproduce the loop, because tailscaled runs outside the rig's network namespace, so the warning is the check that finds a regression on a client.

### Netstack

gVisor `stack.New` with the ipv4, ipv6, tcp, udp, and icmp protocols. NIC 1 is a link endpoint fed from the TUN device. `SetPromiscuousMode(1, true)` and `SetSpoofing(1, true)` make the stack accept every destination and answer from the destination address. The route table has a default route for IPv4 and one for IPv6 through NIC 1.

`tcp.NewForwarder` handles a new TCP flow: it opens a mux TCP stream; a non-zero status completes the request with a reset; a zero status creates the endpoint and copies both ways with half-close. `udp.NewForwarder` handles a new UDP flow, keyed by the 4-tuple: one mux UDP stream per flow, datagrams to and from frames, and the idle timeout closes both sides.

Implementation notes, measured against the pinned gvisor on 2026-09-11. Read and write the TUN device in batches of `dev.BatchSize()`, which is 128 on Linux; a one-buffer loop drops the rest of a GRO super-packet, and batching the writes lets the device coalesce the netstack's segments into one kernel write. Allocate 128 buffers of `offset+65535`, with the read and write offset at least 10 for the virtio-net header, because `CreateTUN` enables GSO. The NIC needs both `SetPromiscuousMode` and `SetSpoofing` and no `AddProtocolAddress` call. Handle each TCP `ForwarderRequest.CreateEndpoint` in its own goroutine, because it runs the handshake and blocks the dispatch path; the UDP `CreateEndpoint` runs inline. The pinned gvisor UDP forwarder handler is `func(*udp.ForwarderRequest) bool`; return true, or the stack sends an ICMP port-unreachable. Release both `pkt.ToView()` and `pkt.DecRef()`, or the buffer pool grows.

ICMP echo has no forwarder in the netstack. The TUN pump reads the protocol field before the injection and takes an ICMPv4 echo request, type 8, and an ICMPv6 echo request, type 128, out of the netstack path; a fragment and a packet with an IPv6 extension header stay on the netstack path. The pump keys a flow by the pinged address and the identifier, queues the request, and the flow's goroutine opens the echo stream and sends the sequence, the TTL, and the payload. For a reply the client builds the echo reply packet, IPv4 or IPv6 header plus ICMP with the checksums, from the pinged address to the original source, and queues it on the link endpoint, so the TUN write pump carries it with the packets of the netstack. Measured before the flow existed, 2026-09-12 on a client: 6 echo requests, 0 replies, and no reply from the netstack itself. gVisor's `RawFactory` was the alternative for the capture; the pump check needs no raw endpoint and no extra goroutine per packet, so it stays.

Traceroute works through the same paths. A UDP flow reports the TTL of each captured datagram, which the UDP endpoint exposes through `SetReceiveTTL` and `SetReceiveHopLimit`, and the frame carries it to the helper. An ICMP error the helper returns, a Time Exceeded from a hop or a Destination Unreachable, becomes a packet the client builds: the outer header from the sender of the error to the original source, the ICMP error header with the type, the code, and the info word, and the original packet rebuilt from the flow, the IP header and the UDP header with the ports and the length of the last datagram, or the IP header and the quoted ICMP bytes, cut to 576 bytes for IPv4 and 1280 bytes for IPv6. The application's kernel then matches the error to the socket by the quoted headers, as it does for a real error. This covers `traceroute` in its UDP and ICMP modes, `mtr`, and `ping -t`. TCP probes, `traceroute -T`, get no Time Exceeded, because the helper's TCP socket does not carry a TTL per segment.

### DNS on Linux

* The mode default is `split` when the manifest has domains, otherwise `none`. The config `defaults.dns` and the `--dns` flag override it.
* systemd-resolved is available when `resolvectl status` exits 0.
* `split`: `resolvectl dns tj0 <servers>`, `resolvectl domain tj0 ~d1 ~d2`, `resolvectl default-route tj0 false`.
* `all`: `resolvectl dns tj0 <servers>`, `resolvectl domain tj0 ~. <search domains>`, `resolvectl default-route tj0 true`.
* Revert: `resolvectl revert tj0`.
* Without resolved, `all` writes `/etc/resolv.conf` with the servers and the search domains. When `/etc/resolv.conf` is a symlink, the session records the target in `/run/tj/resolv.conf.link` and recreates the symlink on revert. Otherwise it copies the file to `/run/tj/resolv.conf.backup` and copies it back on revert. `split` without resolved is an error.
* The servers default to the discovered resolvers of the remote, without the MagicDNS addresses `100.100.100.100` and `fd7a:115c:a1e0::53`.
* `split` needs the manifest `dns.domains`. Without them, `split` is an error that names the missing key.

### Session networks

A pure function in `internal/manifest`, with `go4.org/netipx` for the prefix arithmetic:

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

The result is the minimal sorted prefix list. `internal/cli`'s `sessionNetworks` gathers these inputs from the manifest, the discovery result, the local config, the resolved remote, and the `--network` and `--exclude` flags. It then calls `ComputeNetworks`. `connect`, `describe`, and `doctor` share it, so they report the same session networks for the same remote. A nil discovery result means discovery did not run, or `--no-discovery` set its networks aside.

`describe` prints the manifest, the discovery result, every exclusion, the final list, the DNS mode, the DNS servers, the DNS domains, and the transport. It resolves the DNS mode and the transport the way `connect` would, with no flag override. A DNS resolution error, for example a split mode without the manifest `dns.domains`, does not fail `describe`. `describe` prints the error text as the DNS mode value. `describe` is the tool that finds this problem.

### Discovery script output

One JSON document on stdout:

```json
{
  "version": "1",
  "hostname": "shared-gateway",
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

* `exec_dir` is the first of `$XDG_RUNTIME_DIR` and `$HOME/.cache/tj` where the script can write and execute a file. The probe runs a compiled test file, not a script, and treats `EACCES` or a non-zero exit as not executable. A write and a `chmod` both succeed on a `noexec` mount, so they are not sufficient signals, verified 2026-09-11. A `sh` script still runs on a `noexec` mount, because the interpreter reads it, so the probe file must be a binary. The value is empty when neither directory can execute. A `memfd` loader runs a binary on a `noexec`-only remote, but it needs a non-`sh` delivery path, so it is out of scope for v1 and recorded as a known option.
* `manifest_path` and `manifest` are absent when the remote has no manifest.
* `resolvers` and `search_domains` come from `/run/systemd/resolve/resolv.conf` when that file exists, else from `/etc/resolv.conf`. The script drops `100.100.100.100` and `fd7a:115c:a1e0::53`. Verified on the shared gateway on 2026-09-11: `/etc/resolv.conf` is in resolved's "foreign" mode with `100.100.100.100`, and `/run/systemd/resolve/resolv.conf` has `10.0.0.2` with `eu-west-1.compute.internal`.
* `cloud` is absent when no metadata service answers within 1 s. AWS uses IMDSv2 with a token. The script needs `curl`, and reports no `cloud` without it.
* The IPv6 VPC CIDR comes from IMDS, not from the connected routes. `ip -6 route show scope link` was empty on the gateway on 2026-09-11, while IMDS listed `2001:db8::/56`. The script reads routes with `ip -j route` for JSON. `base64 -w0` exists on the gateway.
* The script is POSIX `sh` and uses `ip`, `cat`, `base64`, `uname`, `mkdir`, `chmod`, `rm`, and `curl` only. It prints the JSON itself. Every value is a CIDR, an address, a hostname, a path, or base64, so no JSON escape is needed. The client parses the JSON strictly and validates every address and CIDR.

### SSH client

* Dial the remote's IPv4 tailnet address on port 22 with a 15 s timeout.
* Auth: `none` first. Tailscale SSH accepts `none` on the shared gateway, verified 2026-09-11, so the client authenticates with a nil `Auth`. When a server refuses `none`, the client adds the ssh-agent keys at `$SSH_AUTH_SOCK` and a keyboard-interactive handler, because a check-mode server can challenge on either.
* `BannerCallback` prints the banner to stderr. The normal accept case sends an empty banner. The SSH channel that carries the check-mode sign-in URL is not verified, because check mode is off on this tailnet. The client prints the banner and any keyboard-interactive prompt, so the URL reaches the user on whichever channel carries it.
* `HostKeyCallback` uses `CacheDir/known_hosts`, keyed by the remote's hostname. A new key is stored. A changed key is a warning on stderr, and the connection continues. The gateway key type is `ecdsa-sha2-nistp256`, stable across connections.
* The client sends `keepalive@openssh.com` every 30 s. The server answers with `ok=false` and a nil error. That is a completed round trip and a valid liveness signal.
* Tailscale SSH runs a command as `/bin/bash -c`, not a login shell, so `XDG_RUNTIME_DIR` is absent and `HOME` is `/root`. A non-zero exit propagates as `*ssh.ExitError` with `ExitStatus()`. Read a command's output with an explicit check. Under rapid session churn an exec channel returned empty stdout with a nil error once in the spike.

### Remote resolution

`tj connect <remote>` and `tj describe <remote>` accept an alias from the config, a hostname, or a tag such as `tag:example`. Resolution considers only online peers, because the gateways are ephemeral and an old node deregisters on termination.

A tag matches every online peer that carries it. A hostname matches an online peer whose HostName equals the reference, or equals the reference followed by a Tailscale collision suffix, that is a hyphen and a number such as `shared-gateway-1`. Tailscale adds that suffix when a replacement node joins before the old node with the same name has deregistered, for example after an AMI replacement.

When more than one online peer matches, the client does not error. It picks the newest: an exact base-name match wins over a suffixed one; among suffixed matches the highest number wins; the most recently active peer breaks a remaining tie. When no online peer matches, the client errors and lists the online candidates. A tag is the most robust reference across an AMI replacement, because the replacement carries the same tag.

A reconnect attempt resolves the same reference again, with this algorithm, so it follows the remote across a move. A status error, a self node that is not online, or no online match ends the attempt without a dial. The self status is tailscaled's in-map-poll flag, false within seconds of a link-down. A resolved address that differs from the current one is a move: the unit logs `remote moved from <old> to <new>` and writes the new address to the state file at once, before it dials.

### Config file

`$XDG_CONFIG_HOME/tj/config.yaml`, schema version 1:

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
  evil8:
    host: shared-gateway
    user: root
    dns: all
    protocols: tcp,udp
    networks:
      - 10.0.0.0/8
    exclude:
      - 10.1.0.0/24
```

The SSH user defaults to the local username. The precedence is the flag, then `remotes.<name>`, then `defaults`. A remote's host is the base hostname; resolution tolerates a Tailscale collision suffix. Prefer a tag for a gateway that an AMI replacement recreates.

`reconnect_for` follows the same precedence: `--reconnect-for`, then `remotes.<name>.reconnect_for`, then `defaults.reconnect_for`, then 10 minutes. `--reconnect-for 0` turns the reconnect off.

A remote's `networks` and `exclude` feed the session network computation: `networks` add to the routed set alongside the manifest, discovery, and the `--network` flags, and `exclude` drops from it alongside `config.exclude`, the manifest exclude, and the `--exclude` flags. See "Session networks". `tj config` writes the defaults and the global exclude; `tj alias` writes the aliases.

`tj config set exclude <cidr>[,<cidr>...]` replaces the global exclude list. `tj config unset <key>` clears one key: `defaults.user`, `defaults.dns`, `defaults.transport`, `defaults.protocols`, `defaults.reconnect_for`, or `exclude`. An unknown key is a usage error, exit code 2, and the message lists the valid keys.

`tj alias unset <alias> <field>...` clears one or more fields of an alias: `user`, `dns`, `transport`, `protocols`, `networks`, `exclude`, or `reconnect_for`. An alias needs `host`, so `unset` does not clear it. An unknown field, or `host`, is a usage error, exit code 2, and the message lists the valid fields. The `--network` and `--exclude` flags of `tj alias set` replace the whole list. They do not add to it.

`config.Load` decodes the file with unknown fields rejected. It then validates the file:

* `version` is absent or 1.
* Each DNS, transport, and protocols value, in `defaults` and in every remote, is valid or empty.
* Each `reconnect_for` value, in `defaults` and in every remote, is empty or a Go duration of zero or more, for example `10m`.
* Each CIDR in `exclude`, in every remote's `networks`, and in every remote's `exclude`, parses.
* Every remote has a `host`.

The error names the file path and the key, for example `remotes.gw.dns: invalid value "bogus", want none, split, or all`. Every command that loads the config reports a broken file at once, exit code 1, except `tj list`, which turns the error into a warning and continues (S2).

A CLI write of `tj config` or `tj alias` replaces the whole file, so it drops the comments of a file an engineer edited by hand. Edit the file directly to keep the comments. Run `$EDITOR $(tj config path)` to open it. The next command that loads the file validates it.

### CLI conventions

* `tj --help` groups the visible commands: Session commands `connect`, `disconnect`, `status`, `logs`; Inspection commands `list`, `describe`, `doctor`, `bench`; Configuration commands `alias`, `config`, `setup`. `completion`, `help`, and `version` stay under Additional Commands.
* Human output through `text/tabwriter`. `--json` on `list`, `describe`, `status`, `doctor`, `bench`, and `connect --dry-run`.
* Errors are one line on stderr, `Error: <message>`. The exit code:

| Code | Meaning |
| ---- | ------- |
| 0 | Success. `tj status` with no active session also exits 0. `tj connect` to the remote of the active session, without `--replace`, also exits 0. |
| 1 | Runtime error: an error that the `RunE` of a command returns. `tj doctor` exits 1 when a check fails. |
| 2 | Usage error: an unknown command, a wrong number of arguments, an unknown flag, or an invalid flag value. |
| 3 | `tj connect` finds an active session to a different remote and has no `--replace`. |
| 130 | SIGINT stopped the command. |
* `log/slog` with a text handler on stderr. `-v` enables debug. The session unit logs to the journal through stderr.
* `_remote` and `_session` are hidden commands.
* `tj logs` prints the session log with `-n/--lines` (default 100, 0 for all) and `-f/--follow`. It works with no active session, and shows the log of the last session, the main use after a failed connect. The hidden `_session logs` adds `--since` (RFC 3339), for the stream and the failure tail of a connect.
* `tj status` adds `RTT:` and `Traffic:` below `Path:` while the session has metrics. `RTT:` is the round-trip time of the transport and the helper, under the load of the session. `Path:` is the latency of the WireGuard path to the remote alone. It needs a disco ping only while the status is up, the rule of `tj list --path`. `--json` adds `metrics` and `path.latency_ms` the same way. A state file from an older root copy has no metrics, so these rows and JSON keys stay absent until `tj setup` installs the new root copy and a new session starts.
* `tj doctor` runs the client checks: `tj version`, `tailnet`, `root copy`, `systemd-run` and `/dev/net/tun` on Linux, and `systemd-resolved`. `tj doctor <remote>` adds the remote checks: peer, SSH ok, discovery ok, manifest path or absent, exec dir, helper arch, parse manifest, session networks non-empty, session networks, DNS default mode, the QUIC transport, and the echo socket of the remote: `raw socket`, `ping socket`, or `none`, with the `ping_group_range` value for the last two. Each row has a status of `ok`, `fail`, or `info`, and a `fail` row sets the exit code to 1. The manifest `checks` field is not implemented yet.
* `tj connect` to the active session, without `--replace`, prints `session to <remote> already up (<uptime>); use --replace to restart` on stdout and exits 0. The check matches the address of the plan, or the reference of the plan while the status is `reconnecting`, because a move leaves the old address in the state file until an attempt resolves the new one. `tj connect` to a reconnecting session then prints `session to <remote> is reconnecting (attempt <n>); use --replace to restart` and exits 0. An active unit without a state file exits 3. `--replace` ends the session and starts a new one.
* `tj connect --reconnect-for <duration>` sets the reconnect window for one connect. See "Config file" for the precedence and the validation.
* `tj connect --dry-run` runs every step of the connect flow through the plan, prints it, and exits 0. It prints no header line. It does not check for an active session and does not call sudo. The human layout matches `describe`: Remote, User, Transport, QUIC ports, Protocols, Reconnect for, DNS mode, DNS servers, DNS domains, Helper arch, Networks. `--dry-run --json` prints the plan JSON, the same bytes `_session start` reads from stdin. `--json` without `--dry-run` is a usage error. The check runs before the remote resolution, so it needs no network access.
* Timeouts: SSH dial 15 s, discovery exec 20 s, helper handshake 10 s, connect 90 s in total.
* `tj list --path` sends up to 3 disco pings per remote through the local API, 200 ms apart, within 2 s, and stops at the first direct pong. A ping can time out while the remote moves data at the link rate: spike 9 measured 4 timeouts of 3 s during downloads at 550 Mbit/s with the session fine at the same moments. The flap detector of the session reads the status endpoint every 10 s and sends no ping, so it is unaffected.
* `tj list --probe` opens SSH to at most 8 peers at a time, and prints them in the tailnet status order regardless of which probe finishes first. It caches no probe result: each run opens SSH again.
* `tj bench <remote>` measures the throughput of the transport to the remote: the duration up, then the duration down, between the client and a temporary helper. It needs no session and no root, and it changes no active session. It follows `tj doctor <remote>`: it resolves the remote, dials SSH, runs discovery, and starts the temporary helper, then it measures. `--transport auto|quic|ssh` has the precedence of `connect`; `auto` falls back to the SSH transport with a reason, and `quic` fails when the QUIC transport does not come up. The congestion controller is BBR in both directions; the manifest `transport.bandwidth` is ignored on purpose, because Brutal would measure the configured rate, not the path. `--duration` sets the time of each direction, 1s to 30s in whole seconds, default 5s; any other value is a usage error, exit code 2. The command has a time limit of 60s plus twice the duration plus 30s per direction; past it, or on an interrupt, it closes the SSH connection so a remote that does not answer cannot hang the command. The two rates are the values for `transport.bandwidth` in the remote's manifest. An active session to the same remote shares the path and lowers the result.

## Testing

* Unit tests are next to the code. The `fake` platform serves `session` and `dns`.
* The loopback test in `internal/dataplane` runs the netstack, the mux client, and the helper in one process over `net.Pipe()`. It sends TCP to a local listener and UDP to a local echo server, feeds an echo request for 127.0.0.1 and for ::1 to the capture and checks the reply packet, and checks that a set without udp answers a datagram with a port unreachable and a set without tcp answers a SYN with a reset. It needs no device and no root. The echo tests need the ping socket, so they skip with the `ping_group_range` value when the runner has neither it nor `CAP_NET_RAW`.
* `TestCountersGrow` in `internal/dataplane/dataplane_test.go` feeds one packet through the pumps and reads `Counters` after each pump ran. It checks that `up` matches the size of the packet the test sent. It also checks that `down` matches the size of the port-unreachable reply the netstack wrote back.
* The sampler tests in `internal/session/metrics_test.go` run the sampler on a fake clock. `TestMetricsWatchSendsTheFirstSampleAtOnce` checks that the first sample follows the first probe. `TestMetricsWatchSamplesOnTheWriteInterval` checks that later samples follow the 5 s write interval, with the counter deltas of that interval. `TestMetricsWatchMeanRTTSkipsFailedProbes` checks that the round-trip time is the mean of the probes that answered, and absent after every probe fails. `TestMetricsWatchDropsASampleWhenTheChannelIsFull` checks that a full channel drops a sample without losing the counts of the tick after it. `TestRunnerWaitStoresASample` checks the single-writer rule: the runner goroutine takes a sample from the channel and writes it to the state file.
* The status row tests in `internal/cli/status_test.go` check `writeStatus` and the JSON output. `TestWriteStatusPrintsTheMetrics` checks the `Path:`, `RTT:`, and `Traffic:` rows of a session with a sample. `TestWriteStatusWithoutMetrics` checks that a state file with no metrics prints neither `RTT:` nor `Traffic:`. `TestStatusJSONHasTheMetrics` checks that `--json` carries the `metrics` object and the path latency. `TestFormatRTT` checks the millisecond format, one decimal below 10 ms and none at or above it.
* The mux loopback test in `internal/mux` pings 127.0.0.1 and ::1 through the helper in one process, and sends a datagram to a closed loopback port, which the kernel answers with a port unreachable that comes back as an error frame while the flow stays open.
* The QUIC loopback test in `internal/mux` negotiates the transport over a yamux loopback and dials a helper listener on 127.0.0.1 in one process. It checks a TCP echo, a UDP echo, a refused second connection, a rejected wrong fingerprint on each side, a refused control stream, and a freed port after `quic-abandon` and after `quit`.
* The lane test in `internal/session` serves two helpers in one process over `net.Pipe()` as the tcp lane and the udp lane of a lane dialer, and checks that the TCP echo and the UDP echo each went through the lane of its protocol. The routing tests next to it use recording dialers and cover the protocol lanes, the dns lane over UDP and over TCP, a port 53 flow to another address, and the fallback to the primary lane.
* The bench test in `internal/mux` runs each direction over a yamux loopback and over a QUIC loopback, and checks that the run moves bytes, that the clock covers the full duration, and that the rate is non-zero. It checks that the client refuses an invalid duration before it sends a request, that the helper's own check refuses an invalid direction and an invalid duration with the other status, and that a cancelled context ends a 30 s run at once and leaves no goroutine behind.
* `test/e2e` is a rootless podman rig: `ubuntu:26.04` with systemd, systemd-resolved, and nftables, run with `--systemd=always --device /dev/net/tun --cap-add NET_ADMIN --cap-add NET_RAW --cap-add SYS_ADMIN`, the tailscaled socket mounted at `/var/run/tailscale/tailscaled.sock`, and the built `tj` mounted. `test/e2e/run.sh` runs it. The rig connects to the shared gateway `shared-gateway` as `root`. It checks that the session comes up on the QUIC transport with the helper port bound to the tailnet address only, that the routes are in table 117 behind the two rules and not in main, that the rules and the table are gone after `disconnect`, TCP over IPv4 and IPv6 to the gateway's VPC addresses, UDP DNS to the VPC resolver, `ping` over IPv4 and IPv6 to the gateway's VPC addresses, because the VPC resolver answers no echo request, `traceroute` in the UDP mode and in the ICMP mode to the gateway's VPC address, which is one hop from the helper, a session with `--protocols tcp,udp` that gets no echo reply while TCP and DNS still work, a session with `--protocols tcp` that refuses `--dns all` and comes up with `--dns none`, the echo socket line of `tj doctor`, each DNS mode, `disconnect`, the one-session lock, and the empty remote. On the SSH transport it checks the lanes: the Transport line of `tj status` with the lane list, the helper process count on the remote, 1 with `--protocols tcp`, 3 with `--dns none`, and 4 on the fallback session with `--dns split`, the private name and `ping` through the fallback session, the empty cache directory on the remote while a session is up, and zero helper processes, files, and listeners after `disconnect`. The count pattern is anchored to the command path, because the Tailscale SSH incubator quotes the exec command on its own command line. With `TJ_TEST_TRACE_TARGET` set to an address outside the VPC, it routes that address through the session and checks that `traceroute` reports a Time Exceeded from at least one hop on the way. It then drops the helper's replies from the UDP range with an nftables input rule inside the rig, which reproduces a tailnet policy without the UDP rule, and checks the fallback to the SSH transport within 5 s plus the SSH baseline, with the warning in the journal, and that `--transport quic` fails without a session. An output-side drop would fail the send at once with EPERM and skip the timeout path. With `TJ_TEST_DERP_REF` set it also connects once to a DERP-relayed remote and checks the QUIC transport there. Verified on 2026-09-11: systemd, resolved, TUN, routes, and the local API work in this rig. Without `CAP_SYS_ADMIN`, resolved fails to start. The rig's user-mode NAT does not carry the DF bit, so a packet size check must run from a client that owns its own route, not from the rig; see spike 5.
* `test/e2e/crash.sh` runs in the same rig and sends SIGKILL to the session process, then checks that the `ExecStopPost` cleanup leaves no device, route, rule, DNS change, or remote file; scenario C does it on the SSH transport with three lanes and checks that no helper process remains on the remote.
* `test/e2e/bench.sh` runs in its own rig and checks `tj bench` on the QUIC transport and on the SSH transport, its `--json` form, its `--duration` range, its fallback to the SSH transport when the QUIC port range is blocked, a run beside an active session that leaves the session up with the same unit invocation and no reconnect, an interrupt that exits 130, and that every run and every disconnect leaves no file, process, or listener on the remote. With `TJ_TEST_DERP_REF` set it also runs once against the relayed remote.
* `test/e2e/reconnect.sh` runs in the same rig and proves that a session rebuilds its transport after a loss. It runs six scenarios on both transports: a helper kill, a 30 s drop of every packet from the remote with `--dns all`, a helper kill with the reconnect off, a disconnect during a reconnect, a drop that outlives a 20 s window, and a gateway that leaves the tailnet through a transient unit on that gateway. Measured on 2026-09-14 in the rig: a helper kill reaches `reconnecting` in 0.4 s and `up` in 0.5 s on the QUIC transport, and in 0.5 s and 0.8 s on the SSH transport. A 30 s drop reaches `reconnecting` in 14 s and `up` 3.8 s after the drop ends on the QUIC transport, and in 13.7 s and 2.1 s on the SSH transport. `tj disconnect` during a reconnect returns in 0.2 s. A 20 s window gives up 35 s after the drop starts. The move scenario ended on `remote changed` in 32 s, because every tagged gateway of the tailnet serves its own manifest.
* `test/e2e/metrics.sh` runs in its own rig, `task e2e:metrics`, and checks the live metrics of `tj status` on both transports: the `RTT:` and `Traffic:` rows and the latency in the `Path:` row within 5 s after the connect, rates above zero during a `ping` burst to the gateway's VPC address, totals that grow by the size of that burst, a new `updated_at` within 7 s, the mode `644` of the state file, no leftover temp file, 50 reads of `tj status --json` in a row that all parse, and the empty runtime directory and the empty remote after `disconnect`. It then kills the helper on the QUIC transport and checks that the totals do not go down and that the `RTT:` row comes back after the reconnect. With `TJ_TEST_DERP_REF` set it also reads the rows once on the relayed remote. It does not write to `/etc/tj` on the remote, so a gateway with a manifest can serve as its target. Measured on 2026-09-17 in the rig, from the `session reconnected after` line of the session log: after a helper kill on the QUIC transport the session with the sampler was up again in 1 s in 28 of 32 rounds and in 2 s to 6 s in the other 4, and a build without the sampler in 1 s in 11 of 20 rounds and in 2 s to 4 s in the other 9, so the sampler adds no delay to a reconnect.
* `test/e2e/loss.sh` is the loss job of chunk 3. It starts the bench server from `test/e2e/loss/bench` on the remote's VPC address, connects from the rig on each transport, then adds `tc netem` loss and delay on the rig's own interface in both directions, egress on the interface and ingress through an `ifb` device, so the host and the remote carry no shaping. The shaping goes on after the session is up, so the numbers describe the data plane and not the SSH bootstrap. It measures 20 TCP connects through the session, 20 UDP DNS queries with a 2 s limit and the p50 and p95 of their query time, and a bulk download, median of 3, with a 60 s time box per run. It also reads the `Reconnects:` row of `tj status` and counts the `session lost` lines in the journal, and its RESULT line adds `reconnects=<n> lost=<m>` per transport. At 7% loss and 30 ms delay with the 20 s yamux write timeout, three 10 minute runs had zero losses, two on the QUIC transport and one on the SSH transport. With the 10 s write timeout the QUIC transport had one yamux keepalive timeout in two 10 minute runs. At 30% loss both transports lose the mux within 30 s to 100 s. The knobs are `TJ_TEST_LOSS_PCT`, `TJ_TEST_DELAY_MS`, `TJ_TEST_TRANSPORTS`, `TJ_TEST_BULK_MIB`, and `TJ_TEST_TARGET=derp` for the relayed remote of `TJ_TEST_DERP_*`. The results are in `docs/spikes/06-loss-test.md`.
* `test/e2e/compare.sh` is the tj against sshuttle comparison of spike 9, `task e2e:compare`. It runs on the client, not in the rig, because the rig cannot run the real direct path, one tunnel at a time: tj on QUIC, tj on the SSH transport, and sshuttle with the flags of the Evil8 `connect` task plus `--dns`. Per session it measures a time-boxed download with probes every 5 s: a DNS query over UDP and over TCP, an ICMP echo, a TCP connect to the remote's tailnet SSH port that does not go through the session, a TCP connect through the session, and a disco ping; then 20 TCP connects, 10 DNS queries, and the system resolver. It reads the remote from `test/e2e/target.env` or from the file in `TJ_TEST_ENV`, and the knobs are `TJ_TEST_SESSIONS`, `TJ_TEST_CYCLES`, `TJ_TEST_DL_SECONDS`, `TJ_TEST_SSHUTTLE_NETS`, and `TJ_TEST_SSHUTTLE_BUF`; `TJ_SSH_LANES=1` in its environment reaches `tj connect`. sshuttle needs a NOPASSWD sudo rule for its firewall helper. The results are in `docs/spikes/09-tj-against-sshuttle.md` and `docs/spikes/10-ssh-lanes.md`.
* `test/e2e/datagram` is the spike tool of chunk 4. It runs a QUIC server and client in one process over 127.0.0.1 and compares the stream flow protocol with QUIC datagrams, shaped with `tc netem` on `lo`. It is measurement code and no part of tj; `docs/spikes/07-udp-datagrams.md` has the result and the decision to keep the stream path.
* `TJ_QUIC_CONTROLLER=cubic` is a measurement knob on `tj connect`. It puts `cubic` in the plan and in the control line, and both sides then leave the library's Cubic in place instead of BBR or Brutal. It exists for the controller comparison of the loss job and has no other use.
* `TJ_SSH_LANES=1` is a measurement knob on `tj connect`. It puts `single_lane` in the plan, and the session then keeps the SSH transport on the primary lane, so a run compares the transport with the lanes and without them. It has no other use.
* `tj connect` never runs on the client machine during development. The client has an sshuttle session, and the one-session rule applies. The rig is the place for every connect test. The exception is `test/e2e/compare.sh`, which the user starts on the client on purpose.

## Performance, spike 1, 2026-09-11

Download of 256 MiB, median of 3 runs, from the shared gateway over a WiFi client. The raw SSH row alone spans 49 to 64 Mbit/s across runs, so the settings were compared as paired cycles, not single medians.

| Path | Mbit/s |
| -- | -- |
| Tailnet direct, no SSH | 76.6 |
| Raw SSH channel | 53.7 |
| tj IPv6, 4 MiB window | 65.9 |
| tj IPv4, 4 MiB window | 51.8 |
| tj IPv4, 256 KiB window | 33.0 |
| sshuttle 1.3.2 | 8.5 |

tj reaches 96% of the raw SSH channel and 6.1 times sshuttle. The only setting with a measurable effect is the yamux `MaxStreamWindowSize` at 4 MiB; the 256 KiB default equals the path's bandwidth-delay product. The netstack receive buffer and SACK have no effect, because the only connection the netstack terminates is the lossless local leg over the TUN. One 256 MiB download costs the client 7.1% of one core and the helper 1.7%. The helper upload takes 291 ms, which is 90 Mbit/s.

The QUIC transport numbers are in `docs/spikes/05-quic-transport.md`: on the direct path a raw QUIC download from the rig reached 1.9 times the same-day raw SSH channel, and under 7% loss BBR carried 154 times what Cubic carried.

## Performance, chunk 3, 2026-09-12

Through a tj session from the rig to the shared gateway, measured by `test/e2e/loss.sh` on a WiFi client, median of 3 downloads of 256 MiB and 20 TCP connects through the session. The loss rows have `tc netem` 7% loss and 30 ms delay in each direction inside the rig. The full tables and the tuning decisions are in `docs/spikes/06-loss-test.md`.

| Path | QUIC, BBR | SSH transport |
| -- | -- | -- |
| Bulk IPv4, clean | 231.7 Mbit/s | 81.1 Mbit/s |
| Bulk IPv6, clean | 207.8 Mbit/s | 70.8 Mbit/s |
| Bulk IPv4, 7% loss | 137.9 Mbit/s | 26.1 Mbit/s |
| Bulk IPv6, 7% loss | 133.9 Mbit/s | 34.6 Mbit/s |
| TCP connect p95, clean | 31.4 ms | 34.5 ms |
| TCP connect p95, 7% loss | 94.0 ms | 1211.0 ms |
| Bulk IPv4 to a DERP-relayed remote, clean | 22.2 Mbit/s | 22.3 Mbit/s |

Under loss, Cubic through tj carried 0.5 Mbit/s and Brutal at a configured 50 Mbit/s carried 48.3 Mbit/s. The receive windows of 8 MiB per stream and 20 MiB per connection gained 22% to 55% under loss over the library defaults, and the 4 MiB yamux window on the SSH transport carried 4 times what 512 KiB carried.
