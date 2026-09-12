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
| `internal/mux` | the client side and the helper side of the mux protocol, on the SSH transport and on the QUIC transport |
| `internal/transport` | the transport mode, the QUIC port range, the rate parser, and the controller choice; standard library only |
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
5. `cmd/tjhelper` imports no package that imports `tailscale.com`, cobra, gvisor, or `log/slog`. It imports yamux, the quic-go fork `github.com/apernet/quic-go`, `crypto/tls`, and `internal/congestion`. `encoding/json` enters the helper graph through the fork's TLS fingerprint feature, which tj does not use; the mux package itself still builds its lines with `fmt.Appendf` and parses them with `strconv`. Measured 2026-09-12 with `-trimpath -ldflags "-s -w"`: 6.38 MiB for linux/arm64 and 6.93 MiB for linux/amd64, against 2.25 MiB and 2.30 MiB before the transport. The gate is 8 MiB, and `task helpers` fails at or above it. There is no room for a second large dependency.
6. The fork publishes no usable module tag: its semver tags declare the upstream module path. The pin is a pseudo-version of its `v0.61.0-mod-rename` branch, and Renovate cannot follow it by tag. The reason for the fork is `Conn.SetCongestionControl`, which upstream does not export; see `docs/spikes/05-quic-transport.md`.

## Platform interfaces

`internal/platform/platform.go` has one interface per concern. The Linux implementation is in `internal/platform/linux`, the macOS one in `internal/platform/darwin`, and the in-memory one in `internal/platform/fake`. Build tags select the real implementation, and `platform.New()` returns it. A chunk may extend an interface. The fake then follows in the same PR.

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

The client opens two SSH session channels on one connection:

1. Upload: `sh -c 'd="${XDG_RUNTIME_DIR:-$HOME/.cache/tj}"; mkdir -p "$d" && f="$d/tj-helper.$$" && cat > "$f" && chmod 0700 "$f" && echo "$f"'`. The client writes the helper for the remote's architecture to stdin, closes stdin, and reads the path from stdout.
2. Run: `exec <path> _remote`. Stdin and stdout are the mux transport. Stderr is the helper log. The client writes it to its own log.

The helper deletes its own file right after start with `os.Remove(os.Args[0])`, so no file remains after a crash. At exit, the helper removes `$HOME/.cache/tj` when that directory is empty. Tailscale SSH sets no `XDG_RUNTIME_DIR`, verified on the shared gateway on 2026-09-11, so the cache path is the common case.

The architecture map is `x86_64` to `amd64` and `aarch64` to `arm64`. Any other value is the error "unsupported remote architecture".

### Mux protocol v2 on the SSH transport

The SSH transport is the helper's stdin (client to helper) and stdout (helper to client). Protocol v2 differs from v1 in the handshake line, the control verbs of the QUIC negotiation, and the probe stream kind. The client uploads the helper embedded in its own binary, so the two always share one version, and the protocol number is a guard, not a negotiation.

1. Handshake: the helper writes the line `TJ2\n` to stdout at start. The client waits at most 10 s for that line. Any other line before it is an error message.
2. Then both sides run yamux over the transport: the client is `yamux.Client`, the helper is `yamux.Server`. Config: `EnableKeepAlive` on, `KeepAliveInterval` 10 s, `ConnectionWriteTimeout` 30 s, `MaxStreamWindowSize` 4 MiB. The keepalive is the liveness check of the session.
3. The client opens every stream. The first byte of a stream is its kind: `0` control, `1` TCP, `2` UDP, `3` probe. A probe stream gets the status byte `0` and a close; the QUIC transport uses it right after its handshake.
4. A TCP or UDP stream continues with the destination: 1 byte address length (4 or 16), the address bytes, and 2 bytes port, big-endian. The helper answers with 1 byte status: `0` ok, `1` refused, `2` unreachable, `3` timeout, `4` other. On a status other than `0`, the helper closes the stream. On a TCP stream the client waits for the status byte before it sends data. On a UDP stream the client does not wait; see the UDP item.
5. TCP: after the status byte, the stream is the connection. A `Close` on one side is a half-close. The other side reads EOF and can still write.
6. UDP: the client does not wait for the status byte. It sends the destination and then the first datagram frame at once, to save a round trip. A frame is 2 bytes length, big-endian, then the payload; both sides send frames. The helper has one UDP socket per stream, connected to the destination; on a dial error it writes a non-zero status byte and closes, which the client treats as the end of the flow. Each side closes the stream after an idle time: 60 s by default, and 10 s when the destination port is 53. A stream close ends the flow. Measured 2026-09-11: the status wait cost one round trip, 61 ms against 31 ms for a DNS query on a 29 ms path.
7. Control: exactly one control stream, opened first. The helper writes one JSON line: `{"version":"…","goos":"linux","goarch":"…","hostname":"…","pid":…}`. The client can write the line `quit\n`. The helper then closes its QUIC listener and exits. The helper also exits when the transport closes. The other control verbs are the QUIC negotiation below.

The helper dials with a 10 s timeout from the remote's default source address. It opens no listener beyond the QUIC listener of the transport below.

### Transport v2: QUIC over the tailnet

Every TCP connection and UDP flow of a session runs as a stream of one QUIC connection between the client and the helper, over UDP to the remote's IPv4 tailnet address. Tailscale SSH stays the bootstrap, the only trust anchor, the control stream, and the fallback transport. The reason is the relayed path: a remote in a private subnet has no direct Tailscale path, DERP relays every packet, the relay loses packets, and one TCP connection that carries every flow collapses on that loss with head-of-line blocking and one shared congestion window. QUIC recovers per packet and keeps each stream independent. The spike that settled the numbers is `docs/spikes/05-quic-transport.md`, and the spec is K8S-206.

Negotiation on the control stream, after the info line:

1. The client writes `quic <bind-address> <first-port>-<last-port> <controller> <client-fingerprint>\n`. The bind address is the tailnet address the client dialed for SSH. The port range comes from the manifest `transport.quic_ports`, default `7443-7452`, because the helper cannot read the manifest. The controller is `bbr`, or `brutal=<bytes-per-second>` with the helper's send rate from the manifest `transport.bandwidth.down`. The fingerprint is `sha256:<hex>` of the client's DER certificate. This line is the chunk 2 amendment of K8S-206 contract C1, because the C1 line had no room for the range and the controller.
2. The helper generates a self-signed ECDSA P-256 certificate in memory, binds the first free UDP port of the range on the bind address only, never the wildcard address, starts the QUIC listener, and writes `quic <port> <helper-fingerprint>\n`. When no port of the range binds, it writes `quic-unavailable <reason>\n`, and the session uses the SSH transport.
3. The client dials `<bind-address>:<port>` with its certificate, ALPN `tj/2`, TLS 1.3, and a verify function that accepts only the helper certificate with the pinned fingerprint. The helper requires a client certificate and accepts only the pinned client fingerprint, and it refuses every handshake once a connection exists.
4. Right after the handshake the client opens a probe stream, kind `3`, and waits for the status byte. TLS 1.3 lets the client complete the handshake before the helper has verified the client certificate, so only an answered probe proves that the helper accepted the connection. The dial, the probe, and the answer share one 5 s budget.
5. When that budget passes without an answered probe, the client writes `quic-abandon\n`, the helper closes the listener, and the session uses the SSH transport with one warning that names the policy rule, `udp:<first>-<last>`.
6. The helper accepts one QUIC connection per session. Both certificates are in memory only; the helper writes no file.

QUIC configuration, on both sides: `InitialPacketSize` 1232 and `DisablePathMTUDiscovery` on, which are mandatory, because the tailnet path MTU is 1280 and the library default of 1280 bytes of payload completes no handshake in either direction, measured in spike 5; `MaxIdleTimeout` 30 s; `KeepAlivePeriod` 10 s; `MaxIncomingStreams` and `MaxIncomingUniStreams` 65536, because the library default of 100 is too low for a tunnel; 0-RTT off; datagrams off; no connection migration. The receive windows are 8 MiB per stream and 20 MiB per connection on both sides, initial and maximum, because spike 6 measured 22% to 55% more throughput under 7% loss than the library defaults through a tj session; spike 5 saw no gain on a raw stream. The UDP socket buffers are the library behaviour: the fork asks for 8 MiB with `SO_RCVBUFFORCE`, which both root processes get, measured in spike 6.

Congestion control: both sides install the controller right after the handshake with the fork's `Conn.SetCongestionControl`. BBR is the default. A manifest with `transport.bandwidth` selects Brutal: the client sends at `up`, the helper at `down`. Brutal sends at the configured rate and compensates loss, which is unfair on a shared relay, so it stays opt-in. Spike 5 measured 261.3 Mbit/s for BBR against 1.7 Mbit/s for Cubic at 7% loss and 30 ms delay.

Flows over QUIC: the client opens every stream, and the first byte is the kind, `1` TCP, `2` UDP, or `3` probe. Kind `0` is refused, because the control stream stays on the SSH transport. The destination, the status byte, the UDP frames, and the idle timeouts are the same as on the SSH transport, and the helper dials the same way. A TCP flow's `Close` on a QUIC stream closes the send side only, and the peer reads EOF; a second `Close` at flow end releases the read side with `CancelRead`. `internal/dataplane` takes a `mux.Dialer` and does not know which transport serves it.

Session lifecycle: the transport is selected right after the mux is up and before the device exists, so `--transport quic` fails early. The yamux keepalive on the SSH transport stays the liveness check of the session. A QUIC connection close ends the session by the same path as a mux close. The stop path closes the QUIC connection, sends `quit`, and the helper closes its listener before it exits; the helper also closes the listener when the SSH transport closes. The state file records `transport`, `quic_port`, and `fallback`, and `tj status` prints `quic (port N)` or `ssh (fallback: <reason>)`. `tj doctor` brings the transport up through a temporary helper, reports `ok, port N` or the reason, and tears it down.

Selection: `tj connect --transport auto|quic|ssh`, then `remotes.<name>.transport`, then `defaults.transport`, default `auto`. `quic` fails the connect when the transport is unavailable, with no fallback; it is the test mode. `ssh` skips the negotiation.

Security: the listener binds the tailnet address only, so it is not reachable from the VPC interface or from the internet. The tailnet policy gates the port range with one UDP rule per remote, for example `{"src": ["group:example"], "dst": ["tag:example"], "ip": ["udp:7443-7452"]}`; without it the session falls back. Mutual TLS pins both certificates by fingerprints that cross the already-authenticated SSH channel, so there is no second trust anchor. The helper accepts one connection per session. Keys and certificates are per session and in memory. The listener closes on `quic-abandon`, on `quit`, and when the SSH transport closes.

Invariants: the tailnet range is excluded from the session networks in every version, so the client's UDP socket to the tailnet address is never routed through `tj0`. The QUIC transport forwards exactly what the SSH transport forwards, the flows to the session networks, and adds no reachability. No file, listener, or process remains on the remote after a session, on every exit path.

## The session

### Privilege on Linux

`tj setup` copies the running binary to `/usr/local/libexec/tj/tj` (root, 0755) and writes `/etc/sudoers.d/tj` with `<user> ALL=(root) NOPASSWD: /usr/local/libexec/tj/tj`. `setup` runs `sudo` interactively once, and validates the file with `visudo -c`. It checks `systemd-run`, `resolvectl`, and `/dev/net/tun`.

The root copy is root-owned, so the NOPASSWD rule does not point at a user-writable file. `tj connect` compares the output of `/usr/local/libexec/tj/tj version` with its own version, and refuses on a mismatch with the message to run `tj setup`.

### Connect flow

1. The unprivileged `tj connect` resolves the remote, opens SSH, reads the manifest, runs discovery, and computes the session networks. It refuses on an empty set. It refuses when a session is active, unless `--replace`.
2. It writes the plan as JSON to the stdin of `sudo -n /usr/local/libexec/tj/tj _session start`. When the effective uid is 0, it runs the same code in-process without sudo.
3. `_session start` writes the plan to `/run/tj/plan.json` (0600) and starts the unit: `systemd-run --unit tj-session --collect --property KillMode=mixed --property TimeoutStopSec=20 --property "ExecStopPost=<root tj> _session cleanup" <root tj> _session run /run/tj/plan.json`. With `--foreground`, it runs `_session run` in-process instead.
4. `_session run` opens SSH, uploads and starts the helper, opens the mux, selects the transport, creates the device, adds the routes, applies the DNS mode, writes the state file with status `up`, and waits for a signal, a mux failure, or a QUIC connection close. On exit it reverts the DNS, removes the routes and the device, closes the QUIC connection, sends `quit` to the helper, and removes the state file.
5. `_session start` waits up to 60 s for the state file with status `up` or for the unit to fail, and prints the result.
6. `tj disconnect` runs `sudo -n /usr/local/libexec/tj/tj _session stop`, which runs `systemctl stop tj-session`.
7. `_session cleanup` runs after every stop. It reverts the DNS on `tj0`, restores `/etc/resolv.conf` from the backup, deletes `tj0` when it exists, and removes the state and plan files. Every step is safe to repeat.

The plan JSON: `{"remote":…,"addr":…,"user":…,"networks":[…],"dns":{"mode":…,"servers":[…],"domains":[…]},"helper_arch":…,"transport":"auto|quic|ssh","quic_ports":"7443-7452","bandwidth_up":0,"bandwidth_down":0}`. The bandwidths are bytes per second, zero for BBR.

The state file `/run/tj/session.json` (0644): `{"remote":…,"addr":…,"user":…,"networks":[…],"dns":{…},"started_at":…,"pid":…,"status":"starting|up|stopping","transport":"quic|ssh","quic_port":…,"fallback":…}`. `tj status` reads it without root.

The lock is the unit name `tj-session.service` plus the state file. macOS uses the pid in the state file instead of a unit.

### Device and routes

The device is `tj0` with MTU 1500. Its addresses are `169.254.117.1/32`, `fd00:117::1/128`, and `fe80::1/64`. The global-scope ULA `fd00:117::1/128` is required as an IPv6 source. RFC 6724 rejects a link-local source for a global destination, so a client without global IPv6 has no source at all without it, measured 2026-09-11. The session adds one `dev tj0` route per session network, IPv4 and IPv6. The session also adds a host route for each DNS server that is outside the session networks, so the queries are captured.

### Netstack

gVisor `stack.New` with the ipv4, ipv6, tcp, udp, and icmp protocols. NIC 1 is a link endpoint fed from the TUN device. `SetPromiscuousMode(1, true)` and `SetSpoofing(1, true)` make the stack accept every destination and answer from the destination address. The route table has a default route for IPv4 and one for IPv6 through NIC 1.

`tcp.NewForwarder` handles a new TCP flow: it opens a mux TCP stream; a non-zero status completes the request with a reset; a zero status creates the endpoint and copies both ways with half-close. `udp.NewForwarder` handles a new UDP flow, keyed by the 4-tuple: one mux UDP stream per flow, datagrams to and from frames, and the idle timeout closes both sides.

Implementation notes, measured against the pinned gvisor on 2026-09-11. Read and write the TUN device in batches of `dev.BatchSize()`, which is 128 on Linux; a one-buffer loop drops the rest of a GRO super-packet, and batching the writes lets the device coalesce the netstack's segments into one kernel write. Allocate 128 buffers of `offset+65535`, with the read and write offset at least 10 for the virtio-net header, because `CreateTUN` enables GSO. The NIC needs both `SetPromiscuousMode` and `SetSpoofing` and no `AddProtocolAddress` call. Handle each TCP `ForwarderRequest.CreateEndpoint` in its own goroutine, because it runs the handshake and blocks the dispatch path; the UDP `CreateEndpoint` runs inline. The pinned gvisor UDP forwarder handler is `func(*udp.ForwarderRequest) bool`; return true, or the stack sends an ICMP port-unreachable. Release both `pkt.ToView()` and `pkt.DecRef()`, or the buffer pool grows.

ICMP is not forwarded in v1, so `ping` through a session does not work. `tj doctor` is the reachability test.

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

The result is the minimal sorted prefix list. `describe` prints the manifest, the discovery result, every exclusion, and the final list.

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

### Config file

`$XDG_CONFIG_HOME/tj/config.yaml`, schema version 1:

```yaml
version: 1
defaults:
  user: root
  dns: split
exclude:
  - 192.168.0.0/16
remotes:
  evil8:
    host: shared-gateway
    user: root
    dns: all
    networks:
      - 10.0.0.0/8
    exclude:
      - 10.1.0.0/24
```

The SSH user defaults to the local username. The precedence is the flag, then `remotes.<name>`, then `defaults`. A remote's host is the base hostname; resolution tolerates a Tailscale collision suffix. Prefer a tag for a gateway that an AMI replacement recreates.

A remote's `networks` and `exclude` feed the session network computation: `networks` add to the routed set alongside the manifest, discovery, and the `--network` flags, and `exclude` drops from it alongside `config.exclude`, the manifest exclude, and the `--exclude` flags. See "Session networks". `tj config` writes the defaults and the global exclude; `tj remote` writes the aliases.

### CLI conventions

* Human output through `text/tabwriter`. `--json` on `list`, `describe`, `status`, and `doctor`.
* Errors are one line on stderr with exit code 1. A usage error exits 2. `connect` exits 3 when a session is active.
* `log/slog` with a text handler on stderr. `-v` enables debug. The session unit logs to the journal through stderr.
* `_remote` and `_session` are hidden commands.
* `tj doctor <remote>` reports: peer online, SSH ok, banner, manifest path or absent, exec dir, helper architecture, discovery ok, session networks non-empty, DNS mode availability, resolved available, sudo rule present, root copy version. It runs the manifest checks from the remote through the helper over a temporary mux, and from the client with a direct dial.
* Timeouts: SSH dial 15 s, discovery exec 20 s, helper handshake 10 s, connect 90 s in total.

## Testing

* Unit tests are next to the code. The `fake` platform serves `session` and `dns`.
* The loopback test in `internal/dataplane` runs the netstack, the mux client, and the helper in one process over `net.Pipe()`. It sends TCP to a local listener and UDP to a local echo server. It needs no device and no root.
* The QUIC loopback test in `internal/mux` negotiates the transport over a yamux loopback and dials a helper listener on 127.0.0.1 in one process. It checks a TCP echo, a UDP echo, a refused second connection, a rejected wrong fingerprint on each side, a refused control stream, and a freed port after `quic-abandon` and after `quit`.
* `test/e2e` is a rootless podman rig: `ubuntu:26.04` with systemd, systemd-resolved, and nftables, run with `--systemd=always --device /dev/net/tun --cap-add NET_ADMIN --cap-add NET_RAW --cap-add SYS_ADMIN`, the tailscaled socket mounted at `/var/run/tailscale/tailscaled.sock`, and the built `tj` mounted. `test/e2e/run.sh` runs it. The rig connects to the shared gateway `shared-gateway` as `root`. It checks that the session comes up on the QUIC transport with the helper port bound to the tailnet address only, TCP over IPv4 and IPv6 to the gateway's VPC addresses, UDP DNS to the VPC resolver, each DNS mode, `disconnect`, the one-session lock, and the empty remote. It then drops the helper's replies from the UDP range with an nftables input rule inside the rig, which reproduces a tailnet policy without the UDP rule, and checks the fallback to the SSH transport within 5 s plus the SSH baseline, with the warning in the journal, and that `--transport quic` fails without a session. An output-side drop would fail the send at once with EPERM and skip the timeout path. With `TJ_TEST_DERP_REF` set it also connects once to a DERP-relayed remote and checks the QUIC transport there. Verified on 2026-09-11: systemd, resolved, TUN, routes, and the local API work in this rig. Without `CAP_SYS_ADMIN`, resolved fails to start. The rig's user-mode NAT does not carry the DF bit, so a packet size check must run from a client that owns its own route, not from the rig; see spike 5.
* `test/e2e/loss.sh` is the loss job of chunk 3. It starts the bench server from `test/e2e/loss/bench` on the remote's VPC address, connects from the rig on each transport, then adds `tc netem` loss and delay on the rig's own interface in both directions, egress on the interface and ingress through an `ifb` device, so the host and the remote carry no shaping. The shaping goes on after the session is up, so the numbers describe the data plane and not the SSH bootstrap. It measures 20 TCP connects through the session, 20 UDP DNS queries with a 2 s limit and the p50 and p95 of their query time, and a bulk download, median of 3, with a 60 s time box per run. The knobs are `TJ_TEST_LOSS_PCT`, `TJ_TEST_DELAY_MS`, `TJ_TEST_TRANSPORTS`, `TJ_TEST_BULK_MIB`, and `TJ_TEST_TARGET=derp` for the relayed remote of `TJ_TEST_DERP_*`. The results are in `docs/spikes/06-loss-test.md`.
* `test/e2e/datagram` is the spike tool of chunk 4. It runs a QUIC server and client in one process over 127.0.0.1 and compares the stream flow protocol with QUIC datagrams, shaped with `tc netem` on `lo`. It is measurement code and no part of tj; `docs/spikes/07-udp-datagrams.md` has the result and the decision to keep the stream path.
* `TJ_QUIC_CONTROLLER=cubic` is a measurement knob on `tj connect`. It puts `cubic` in the plan and in the control line, and both sides then leave the library's Cubic in place instead of BBR or Brutal. It exists for the controller comparison of the loss job and has no other use.
* `tj connect` never runs on the client machine during development. The client has an sshuttle session, and the one-session rule applies. The rig is the place for every connect test.

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
