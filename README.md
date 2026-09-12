# tailjump

`tj` starts a session into a remote network over Tailscale SSH and routes the
remote's networks through a local TUN device, in the style of
[sshuttle](https://github.com/sshuttle/sshuttle). The remote needs Tailscale
SSH and nothing else: no installed software, no subnet routes, and no state
between sessions.

## How it works

* Tailscale SSH authenticates the remote. The session uploads a helper binary
  for its duration, and the helper deletes its own file at start.
* A TUN device from wireguard-go and a gVisor netstack capture the TCP and UDP
  flows on the client.
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
tj connect <remote> --dns split
tj status
tj disconnect
```

`<remote>` is a hostname, a tag, or an alias from `tj remote`. `tj --help`
lists every command and alias.

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
`tj connect` never runs on the development machine.
