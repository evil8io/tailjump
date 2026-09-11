# tailjump (tj) v1 handoff spec

This document is a copy of Linear story K8S-194. The story is the spec and the parent. Each chunk in section 9 is its own story with one PR.

## 1. Goal

`tj` is a CLI that gives an engineer a session into a remote network over Tailscale SSH, the way sshuttle does. The remote runs no installed software and advertises no subnet routes. The engineer starts and ends every session. The tool works in any tailnet and has no Evil8 term in it. It replaces sshuttle plus the `ts-gateway.sh` script and the `connect` tasks in iac-modules.

Repository: https://github.com/evil8io/tailjump (private). Binary name: `tj`. Language: Go.

## 2. Non-goals

* Kubernetes, kubectl, kubeconfig, and any cloud CLI.
* Subnet routers, exit nodes, and `--advertise-routes`.
* A central catalogue of remotes.
* More than one active session.
* Windows clients.
* macOS in v1. The scaffolding for macOS is in v1, see section 8.

## 3. Terms

* **remote**: the tailnet node that a session uses. sshuttle calls it `--remote`.
* **manifest**: the config file that a remote advertises.
* **session**: one active connection from the laptop to one remote.
* **helper**: the tj binary that the client uploads to the remote for the session.
* **session networks**: the set of CIDRs that a session routes.

## 4. Decisions taken

| Decision | Choice | Reason |
| -- | -- | -- |
| Language | Go | One static binary, cross-compile for Linux and macOS, mise install through the `github` backend, Tailscale client libraries |
| Where the config lives | The remote advertises a manifest; no central repository | A remote then needs no central change, and the tool works in any tailnet |
| Manifest shape | The manifest is the profile; no named profiles | One line per remote in `list`, no selector on `connect`, one concept less. Variants come from the SSH user, because the manifest lookup starts in the SSH user's config directory, and the tailnet policy enforces who may use that user |
| Discovery | Its own section with one boolean per source; the session set is the union of discovery and `networks`, minus excludes | No keyword in a list |
| Sessions | One active session per machine; `connect` refuses when one is active | Every customer uses ranges inside 10.0.0.0/8 |
| Data plane | Native from v1, with TCP, UDP, IPv4, IPv6, and DNS over UDP | sshuttle is not a dependency |
| Capture | A TUN device with a userspace network stack (`golang.zx2c4.com/wireguard/tun` and `gvisor.dev/gvisor/pkg/tcpip`), not TPROXY and not pf | One data plane for Linux and macOS, no firewall rules, the capture is a route |
| Remote side | A helper uploaded per session; not SSH `direct-tcpip` | SSH forwards no UDP |
| Privilege | `tj connect` re-executes as root through sudo and runs the session as a transient system unit | Least code for v1; the root-helper split is the upgrade path |
| Client OS | Linux in v1, macOS later, modern OSes only | Scope |

## 5. Contracts

### C1. The remote

A tailnet node is a remote when:

1. Tailscale SSH is on, and the tailnet policy lets the engineer open SSH as some user.
2. The host is Linux on amd64 or arm64.
3. The SSH user has a directory where it can write and execute a file. The lookup order is `$XDG_RUNTIME_DIR`, then `$HOME/.cache/tj`.
4. The node reaches the networks. It needs no IP forwarding, no NAT, no subnet routes, and no root.

A manifest is optional. The client looks in `$XDG_CONFIG_HOME/tj/manifest.yaml` of the SSH user first, then in `/etc/tj/manifest.yaml`. A missing manifest equals an empty manifest.

The client leaves nothing on the remote after a session.

### C2. The manifest, schema version 1

```yaml
version: 1
name: corp-nonprd
description: Corp non-production, eu-west-1
discovery:
  link_routes: true
  cloud_metadata: true
networks:
  - 172.16.0.0/12
  - 145.7.5.0/24
exclude:
  - 172.31.0.0/16
dns:
  servers: [10.1.0.2]
  domains: [corp.example]
checks:
  - name: corp-dns
    tcp: 10.1.0.2:53
```

* `discovery`: an absent section means all sources on.
* `networks`: static CIDRs, IPv4 and IPv6.
* `exclude`: CIDRs that a session never routes.
* `dns.servers`: absent means the discovered resolvers.
* `dns.domains`: absent means the `split` mode is not available, and the client says so.
* `checks`: TCP endpoints that `tj doctor` tests from the remote and from the laptop.

The client always excludes the tailnet range 100.64.0.0/10 and the remote's own address, so the manifest does not list them.

### C3. Discovery

The client pipes a POSIX `sh` script over the SSH session channel, and the script prints one JSON document. Sources:

* `link_routes`: the connected subnets from `ip route`, IPv4 and IPv6.
* `resolvers` and `search_domains`: the per-link upstreams from `resolvectl`, or `/etc/resolv.conf`. The script skips the MagicDNS address 100.100.100.100.
* `cloud`: the network CIDRs from the metadata service of AWS, Azure, GCP, or OCI, when one answers. AWS is verified: the instance metadata path `network/interfaces/macs/<mac>/vpc-ipv4-cidr-blocks` lists the primary and the secondary CIDRs, and `vpc-ipv6-cidr-blocks` the IPv6 CIDR. Azure and OCI metadata report the subnet only, unverified.

The script version is the client version. Discovery cannot list routes beyond the VPC, for example TGW routes in a shared VPC. Those go in `networks`.

### C4. The session

* One session is active at a time. `connect` refuses when a session is active and names it. `--replace` ends the active session first.
* A session ends on `disconnect`, on logout, or on a failed liveness check. It never restarts after a reboot.
* The session networks: the manifest `networks` plus the discovery result, minus the manifest `exclude`, minus the tailnet range, minus the remote's address, minus the laptop's connected subnets, minus the local excludes and the `--exclude` flags.
* The client writes state to the runtime directory and logs to the journal.

### C5. The tailnet policy

The policy needs one SSH rule per remote and user. Nothing else. A tag is optional and only filters `list`.

## 6. Architecture

### Components

1. **CLI**: cobra commands, config, output.
2. **tailnet**: reads the peers from the tailscaled local API with `tailscale.com/client/local`. Peers expose `HostName`, `Tags`, `Online`, and `TailscaleIPs`, verified with `tailscale status --json`.
3. **ssh**: `golang.org/x/crypto/ssh` against the remote's tailnet address. Auth is `none` or any public key, because Tailscale SSH authenticates the node. Check mode sends a URL in the SSH banner, so the client prints the banner. Host keys: the client trusts a tailnet address because WireGuard authenticated the peer, and it stores the key per remote for a warning on change.
4. **manifest and discovery**: read the manifest, run the discovery script, merge, compute the session networks.
5. **platform**: one interface per concern with a Linux implementation and a macOS stub, see section 8.
6. **dataplane**: the TUN device, the netstack, and the flow handler. Every TCP connection and UDP flow that the stack terminates becomes a stream or a datagram sequence on the mux.
7. **mux**: one multiplexed protocol over the SSH session's stdin and stdout, for example `hashicorp/yamux`. One stream per TCP connection. UDP as framed datagrams with a flow id, an original destination, and an idle timeout on both ends.
8. **helper**: the `tj _remote` subcommand. The client embeds the helper for linux/amd64 and linux/arm64, uploads the one for `uname -m`, runs it, and deletes it at the end. The helper is unprivileged. It opens outbound TCP connections and UDP sockets.

### Session flow

1. `tj connect <remote>` resolves the remote by hostname or tag, and checks that it is online.
2. The client opens the SSH connection, reads the manifest, and runs discovery.
3. The client computes the session networks and refuses on an empty set.
4. The client re-executes as root and starts the session as a transient system unit.
5. The session uploads and starts the helper, and opens the mux.
6. The session creates the TUN device, adds one route per session network, and applies the DNS mode.
7. The session runs a liveness check on the mux, and ends on failure.
8. `disconnect` stops the unit. The unit's stop path removes the routes, the device, the DNS config, and the helper file.

### DNS modes

* `none`: no DNS change.
* `split`: the manifest domains resolve at the manifest servers. Linux: a routing domain per domain, for example `~corp.example`, and the servers as the DNS of the `tj0` link, through systemd-resolved. The servers are inside the session networks, so the queries are ordinary captured UDP and TCP.
* `all`: every query resolves at the manifest servers. Linux: `~.` on the `tj0` link through systemd-resolved. Fallback without resolved: rewrite `/etc/resolv.conf` with a backup, and restore on disconnect.
* The MagicDNS address is never captured, so tailnet names resolve locally in every mode.

### Privilege

`tj setup` writes a sudoers rule for `tj` once. `tj connect` re-executes as root and runs the session as a transient system unit with `systemd-run`. The root-helper split with socket passing is the later upgrade.

## 7. The CLI

* `tj setup`: sudoers rule, tool checks.
* `tj list [--tag <tag>] [--probe]`: online peers; `--probe` opens SSH to each with a short timeout and marks the ones with a manifest; results are cached.
* `tj describe <remote>`: the merged config without a session.
* `tj doctor <remote>`: runs the manifest checks from the remote and from the laptop.
* `tj connect <remote> [--user <ssh-user>] [--dns none|split|all] [--exclude <cidr>]... [--no-discovery] [--replace]`
* `tj disconnect`
* `tj status`: the active session, its networks, the DNS mode, the remote, and the uptime.

Local config in `$XDG_CONFIG_HOME/tj/config.yaml`: aliases per remote, default SSH user, default DNS mode, local excludes.

## 8. Platform layer and macOS scaffolding

| Concern | Linux, v1 | macOS, later |
| -- | -- | -- |
| Device | TUN through the wireguard `tun` package, address and MTU over netlink | `utun` through the same package |
| Routes | netlink with `vishvananda/netlink` | the `route` socket |
| DNS `split` | resolved routing domains on `tj0` | one file per domain under `/etc/resolver/` |
| DNS `all` | resolved `~.` on `tj0`, `resolv.conf` fallback | the DNS servers of the active network service, restored on disconnect |
| Session runner | transient system unit with `systemd-run` | a detached child with a state file |
| Privilege | sudo re-exec | the same |
| tailscaled API | `tailscale.com/client/local` over the unix socket | the same package |
| Paths | XDG directories | `~/Library/Application Support` and `~/Library/Caches` |

Scaffolding in v1:

* The build matrix has `linux/amd64`, `linux/arm64`, and `darwin/arm64` from the first commit, and CI builds all three.
* The macOS platform package exists and returns a "not supported yet" error from the device call only. `list`, `describe`, and `doctor` work on macOS in v1.
* The platform interfaces live in one package with a fake implementation for tests.
* The data plane has an in-process test that runs the netstack, the mux, and the helper in one process over a pipe, with no device and no root.
* The remote helper is Linux only in every version. A macOS client embeds the same two Linux helpers.

## 9. Chunks

Each chunk is one story and one PR. The order is the dependency order.

| # | Chunk | Content | Done when |
| -- | -- | -- | -- |
| 0 | Repository scaffold | Go module, cobra skeleton, platform interfaces with fakes and the macOS stub, CI build matrix, lint, release-please, goreleaser, mise install through the `github` backend with a token for the private repo | CI is green on all three targets, and `tj version` runs |
| 1 | Spikes | Four throwaway branches with a written result each in `docs/spikes/`: TUN plus netstack on Linux with a throughput measurement against sshuttle on the same remote; the Go SSH client against Tailscale SSH including check mode; a remote with a `noexec` runtime directory; the helper size | Each result is a document with the numbers |
| 2 | Tailnet and SSH | `list`, `describe`, `doctor`; the local API client; the SSH client; the manifest reader; the discovery script; the session network computation | `tj describe` prints the merged config for the shared gateway and for a second gateway |
| 3 | Helper and mux | `tj _remote`; the mux protocol; TCP streams; UDP frames with idle timeout; upload, start, and cleanup; the in-process loopback test | The loopback test passes for TCP and UDP, and no file remains on the remote |
| 4 | Data plane on Linux | The device, the netstack, the flow handler, the routes; `connect`, `disconnect`, `status`; sudo re-exec; the transient unit; the one-session lock | TCP, UDP, IPv4, and IPv6 reach the VPC CIDRs of the shared gateway |
| 5 | DNS modes on Linux | `none`, `split`, `all`; resolved; the `resolv.conf` fallback | A private name resolves in `split`, and only the listed domain goes to the remote |
| 6 | Hardening and docs | Liveness, cleanup on crash, logs, `setup`, the README with the contracts, the manifest schema document | An interrupted session leaves no route, device, DNS change, or remote file |
| 7 | macOS platform | The macOS implementations of section 8 | The chunk 4 and 5 criteria pass on a modern macOS |
| 8 | Deployment integration in the infrastructure repo | The tailscale-gateway module writes the manifest through user data; the `connect` tasks and `ts-gateway.sh` leave the tree; the mise pin | A session to a customer gateway works with `tj` alone |

Chunk 8 is a separate story in the iac-modules flow.

## 10. Acceptance criteria for v1

* On a current Ubuntu laptop, `tj connect` to the shared gateway gives TCP, UDP, and DNS to the VPC CIDRs, IPv4 and IPv6.
* `tailscale status --json` shows no primary routes on the remote before, during, and after a session.
* No file remains on the remote after `disconnect`.
* `tj connect` with an active session refuses and names the session.
* A session to a remote without a manifest works on its connected subnets.
* A CI build for `darwin/arm64` passes, and `tj list` runs on macOS.

## 11. Verified facts, 2026-09-11

* Tailscale SSH on the shared gateway, Tailscale 1.102.3, accepts an OpenSSH port forward with `-N` and no command. A DNS query over TCP through that forward returned the expected address. The design does not use forwards, but the test proves the session channel and exec.
* The instance metadata on both gateways lists the VPC CIDRs, including the secondary CIDRs, and the IPv6 CIDR. Instance tags in metadata are off. `resolvectl` shows the VPC resolver on the `ens5` link. A gateway can have no IPv6 address.
* The pod CIDRs of both VPCs are secondary VPC CIDRs inside the tailnet range 100.64.0.0/10, so the tailnet exclusion is not optional.
* `tailscale status --json` shows `Tags`, `Online`, and `TailscaleIPs` for peers.
* sshuttle 1.3.2: `--dns` reads the upstreams behind the resolved stub; `--ns-hosts` captures one address; `--to-ns` sets the server target; without it the server picks a random resolver from its own `resolv.conf`; the server does not exclude itself. The client code that tj replaces is about 2,700 lines of Python.

## 12. References

* sshuttle: https://github.com/sshuttle/sshuttle
* sshuttle_rust, a rewrite over `ssh -D` with no server code: https://github.com/sshuttle/sshuttle_rust
* tprosshy, a rewrite with DNS over TCP: https://github.com/weezurd/tprosshy
* Tailscale SSH port forwarding: https://github.com/tailscale/tailscale/issues/5091 and https://github.com/tailscale/tailscale/issues/6575
* Tailscale SSH docs: https://tailscale.com/docs/features/tailscale-ssh
* gVisor netstack: https://pkg.go.dev/gvisor.dev/gvisor/pkg/tcpip
* wireguard-go tun: https://pkg.go.dev/golang.zx2c4.com/wireguard/tun
* tailscaled local API client: https://pkg.go.dev/tailscale.com/client/local
