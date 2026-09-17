# tailjump

`tj` creates a point-to-site VPN tunnel to any Tailscale peer with Tailscale
SSH enabled. It is inspired by [sshuttle](https://github.com/sshuttle/sshuttle).

## Why

Tailscale lets clients [accept routes from subnet routers](https://tailscale.com/kb/1019/subnets).
However, this means accepting all routes advertised by all routers. This makes
it impossible to connect to the networks of Tailscale peers whose networks
have overlapping CIDRs. `tj` acts like a traditional proxy VPN, granting the
client access to a single peer's network. See
[tailscale#18451](https://github.com/tailscale/tailscale/issues/18451),
[tailscale#19590](https://github.com/tailscale/tailscale/issues/19590),
[tailscale#10206](https://github.com/tailscale/tailscale/issues/10206), and
[tailscale#16323](https://github.com/tailscale/tailscale/issues/16323).

## How it works

* Tailscale SSH authenticates the remote. The session uploads a helper binary
  for its duration, and the helper deletes its own file at start.
* A TUN device from wireguard-go and a gVisor netstack capture the TCP, UDP,
  and ICMP echo flows on the client, so `ping` and `traceroute` work through
  the session.
* The flows run as streams of one QUIC connection to the remote's tailnet
  address, with the BBR congestion controller. The SSH channel with yamux is
  the fallback transport.
* A remote can advertise a [manifest](docs/manifest.md) with its networks,
  its DNS servers and domains, and the checks for `tj doctor`. Without a
  manifest the session routes the connected subnets and the cloud VPC.
* The session runs as a transient systemd unit and applies the DNS mode
  through systemd-resolved.

Written in Go. The QUIC library is the apernet fork of quic-go, with the BBR
and Brutal controllers from hysteria.

## Requirements

* A remote with Tailscale SSH on Linux, amd64 or arm64.
* A tailnet policy with an SSH rule to the remote and a UDP rule for the QUIC
  port range, default `7443-7452`. Without the UDP rule the session uses the
  SSH transport.
* A Linux client with systemd and `/dev/net/tun`. The macOS client compiles
  but is not verified.

## Install

```
[tools]
"github:evil8io/tailjump" = "latest"
```

Or take the archive from the [releases](https://github.com/evil8io/tailjump/releases).
A `go install` build embeds no helper, so it serves the read-only commands only.

## Use

```
tj setup
tj list --path
tj bench <remote>
tj connect <remote> --dns split --protocols tcp,udp,icmp
tj status
tj logs
tj disconnect
```

`<remote>` is a hostname, a tag, or an alias from `tj alias`. `tj --help`
lists every command in its group. `tj connect --dry-run` prints the plan for
a remote and starts no session.

`--json` prints machine output on `list`, `describe`, `status`, `doctor`,
`bench`, and `connect --dry-run`.

`tj bench <remote>` measures the throughput of the transport to a remote, up
and then down, without a session. Its rates are the values for
`transport.bandwidth` in the remote's manifest.

Exit codes:

* `0` is success. A connect to the remote of the active session, without
  `--replace`, also exits 0.
* `1` is a runtime error.
* `2` is a usage error.
* `3` is an active session to a different remote, without `--replace`.
* `130` is an interrupt.

A session reconnects after a loss for `reconnect_for`, default 10 minutes.
`--reconnect-for 0` turns the reconnect off. `tj status` shows the reconnect.

`tj` completes remotes, aliases, config keys, and flag values. Install the
completion script for your shell.

```
tj completion bash > /etc/bash_completion.d/tj
tj completion zsh > "${fpath[1]}/_tj"
tj completion fish > ~/.config/fish/completions/tj.fish
tj completion powershell > tj.ps1
```

## Docs

* [`docs/spec.md`](docs/spec.md): the v1 spec.
* [`docs/architecture.md`](docs/architecture.md): the architecture decisions,
  the protocols, and the test rig.
* [`docs/manifest.md`](docs/manifest.md): the manifest schema.
* [`docs/spikes/`](docs/spikes/): the measurements behind the decisions.

## Develop

```
mise install
task build
task test
task lint
task e2e
```

`task e2e` runs the podman rig against the gateway in `test/e2e/target.env`.
`test/e2e/reconnect.sh` runs in the same rig and checks that a session
reconnects after a loss.
`tj connect` never runs on the development machine.
