# tailjump

`tj` gives an engineer a session into a remote network over Tailscale SSH,
the way [sshuttle](https://github.com/sshuttle/sshuttle) does. Start a session with one command, reach every
network the remote gives you, and end it with one command; the remote runs
no installed software, advertises no subnet routes, and keeps no state
between sessions.

`tj` works in any tailnet. It has no dependency on a particular company or
cloud, and no central catalogue of remotes: a remote optionally advertises a
[manifest](docs/manifest.md), and `tj` reads it over the same SSH connection
it uses for the session.

Read [`docs/spec.md`](docs/spec.md) for the full v1 handoff spec and
[`docs/architecture.md`](docs/architecture.md) for the binding architecture
decisions. This README covers the parts an engineer needs day to day.

## Non-goals

`tj` is not a Kubernetes tool, not a cloud CLI, not a subnet router or exit
node, and not a VPN with a central control plane. It runs one session at a
time, and it has no Windows client. See `docs/spec.md`, section 2, for the
full list.

## Install

### Prebuilt binary (recommended)

Download the archive for your OS and architecture from the
[releases page](https://github.com/evil8io/tailjump/releases), then put `tj`
on your `PATH`:

```
tar -xzf tj_<version>_<os>_<arch>.tar.gz tj
sudo install tj /usr/local/bin/tj
```

The release binary ships with the remote helper built in.

### With mise

```toml
[tools]
"github:evil8io/tailjump" = "latest"
```

### From source

```
go install github.com/evil8io/tailjump/cmd/tj@latest
```

A source build embeds no remote helper, so `tj connect` needs a release
build or a local `task helpers` run. The read-only commands `tj list`,
`describe`, and `doctor` work either way.

While the repository is private, the prebuilt and mise options need a
`GITHUB_TOKEN` with read access in the environment.

## Setup

Run this once per machine:

```
tj setup
```

`setup` checks that `systemd-run` and `/dev/net/tun` are present, and warns
when `resolvectl` is absent (only the `split` and `all` DNS modes need it).
It then copies the running binary to `/usr/local/libexec/tj/tj` and writes
`/etc/sudoers.d/tj`, so `tj connect` and `tj disconnect` can start and stop
a session as root without a password prompt on every use. It runs `sudo`
interactively once and may ask for your password.

## Commands

| Command | Does |
| -- | -- |
| `tj setup` | Install the sudoers rule and the root copy; check the required tools. |
| `tj list [--tag <tag>] [--probe]` | List the online tailnet peers. `--probe` opens SSH to each and marks the ones with a manifest. |
| `tj describe <remote> [--user <user>] [--no-discovery]` | Print the merged manifest, discovery result, and computed session networks for a remote, without starting a session. |
| `tj doctor <remote>` | Report readiness facts for a remote: peer online, SSH ok, manifest present, discovery ok, the computed session networks, and DNS mode availability. |
| `tj connect <remote> [--user <user>] [--dns none\|split\|all] [--exclude <cidr>]... [--no-discovery] [--replace]` | Start a session into the remote's network. |
| `tj disconnect` | End the active session. Not an error when none is active. |
| `tj status [--json]` | Print the active session: the remote, the DNS mode, the networks, and the uptime. |
| `tj version` | Print the tj version. |

`<remote>` is a hostname, a tag such as `tag:example`, or an alias from the
local config. `list`, `describe`, `status`, and `doctor` also accept
`--json`. `-v` on any command enables debug logging.

`tj connect` exits 3, and names the active session, when one is already
running; use `--replace` to end it first. Every other error exits 1, and a
usage error exits 2.

### Example

```
$ tj list
HOSTNAME     TAGS            ADDRESS
gw.example   tag:example     100.64.0.10

$ tj connect gw.example --dns split
session to gw.example up, 3 networks

$ tj status
Remote:      gw.example (100.64.0.10)
User:        root
Status:      up
DNS mode:    split
Uptime:      4m12s
Networks:    10.0.0.0/16, 2001:db8::/56, 10.1.0.2/32

$ tj disconnect
```

## The manifest

A remote optionally advertises a manifest at `$XDG_CONFIG_HOME/tj/manifest.yaml`
or `/etc/tj/manifest.yaml` on the SSH user. The manifest names the networks
a session routes, the DNS servers and domains, and the checks `tj doctor`
runs. See [`docs/manifest.md`](docs/manifest.md) for the full schema
reference. A remote with no manifest still works, on its connected subnets
and cloud metadata alone.

## Contracts, in short

* **A remote** needs Tailscale SSH, a Linux host on amd64 or arm64, and a
  directory the SSH user can write and execute a file in. It needs no IP
  forwarding, no NAT, no subnet routes, and no root. `tj` leaves nothing on
  it after a session; see `docs/spec.md`, contract C1.
* **A session** is one active connection at a time; `connect` refuses a
  second one and names the active one. A session ends on `disconnect`, on
  logout, or on a failed liveness check, and it never restarts after a
  reboot. See contract C4.
* **The tailnet policy** needs one SSH rule per remote and user, and
  nothing else; a tag is optional and only filters `list`. See contract C5.

## DNS modes

* `none`: no DNS change. The default without manifest domains.
* `split`: the manifest domains resolve at the manifest servers; everything
  else resolves as it did before the session. Needs `dns.domains` in the
  manifest.
* `all`: every query resolves at the manifest servers.

`tj` never captures MagicDNS traffic, so tailnet names always resolve
locally, in every mode.

## Develop

```
mise install
task build
task test
task lint
```

`task build` runs `task helpers` first, which cross-compiles the remote
helper into `internal/helper/embed/bin`, then builds `bin/tj` for the host.

`tj connect` never runs on a developer machine: the one-session rule
applies there too, and a laptop typically already has its own session to
somewhere else. `test/e2e` is a rootless podman rig for exactly this: it
builds `tj`, starts an `ubuntu:26.04` container with systemd, and connects
from inside it against a real gateway. Set the real gateway in
`test/e2e/target.env` (git-ignored; see `test/e2e/target.env.example`), then
run `test/e2e/run.sh` for the reachability and DNS tests, or
`test/e2e/crash.sh` for the interrupted-session cleanup test.

## Docs

* [`docs/spec.md`](docs/spec.md): the v1 handoff spec.
* [`docs/architecture.md`](docs/architecture.md): the binding architecture decisions.
* [`docs/manifest.md`](docs/manifest.md): the manifest schema reference.
