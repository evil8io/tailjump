# The manifest, schema version 1

A remote advertises a manifest to describe itself: the networks it gives a
session, the DNS it offers, and the checks `tj doctor` runs. The manifest is
optional. `tj` looks for it in two places, in order, on the SSH user the
session connects as:

1. `$XDG_CONFIG_HOME/tj/manifest.yaml`
2. `/etc/tj/manifest.yaml`

A missing manifest is an empty manifest: `tj` still connects, using only the
discovered networks (see "Discovery" below).

## Full example

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
transport:
  quic_ports: "7443-7452"
  bandwidth:
    up: 20 mbps
    down: 50 mbps
```

## Fields

| Field | Type | Required | Meaning |
| -- | -- | -- | -- |
| `version` | integer | yes | The schema version. `tj` accepts `1` and refuses any other value. |
| `name` | string | no | A label for the remote. `tj describe` and `tj list --probe` show it; it has no effect on the session. |
| `description` | string | no | A longer label, shown the same way as `name`. |
| `discovery.link_routes` | boolean | no | Turns the connected-subnet source off. Default `true`. |
| `discovery.cloud_metadata` | boolean | no | Turns the cloud metadata source off. Default `true`. |
| `networks` | list of CIDR | no | Static networks the session always routes, IPv4 and IPv6. |
| `exclude` | list of CIDR | no | Networks the session never routes, regardless of `networks` or discovery. |
| `dns.servers` | list of IP | no | The DNS servers a `split` or `all` session uses. Default: the resolvers discovery finds on the remote. |
| `dns.domains` | list of domain | no | The domains a `split` session sends to `dns.servers`. Required for `split`; `tj connect --dns split` refuses and names the missing key without them. |
| `checks` | list of check | no | TCP endpoints `tj doctor` tests, from the remote and from the client. |
| `checks[].name` | string | yes, inside a check | A label for the check line in `tj doctor` output. |
| `checks[].tcp` | `host:port` | yes, inside a check | The endpoint to dial. |
| `transport.quic_ports` | `first-last` | no | The UDP port range the helper listens in for the QUIC transport, on the remote's tailnet address. Default `7443-7452`. The tailnet policy must pass the range; see "The transport section". |
| `transport.bandwidth.up` | rate | no | The rate from the client to the remote, for example `20 mbps`. A set bandwidth selects the Brutal congestion controller at these rates; an unset one selects BBR. `up` and `down` go together. |
| `transport.bandwidth.down` | rate | no | The rate from the remote to the client, for example `50 mbps`. |

An absent `discovery` section means every source is on. An absent
`discovery.link_routes` or `discovery.cloud_metadata` key, inside a present
`discovery` section, also means that source is on; only an explicit `false`
turns it off.

## The `exclude` list needs no reserved ranges

`tj` always excludes the tailnet range `100.64.0.0/10` and
`fd7a:115c:a1e0::/48`, the remote's own tailnet addresses, and the standard
reserved ranges (loopback, link-local, and multicast, IPv4 and IPv6). A
manifest does not list them.

## How networks and DNS servers combine with discovery

`tj connect` computes the session networks as:

```
manifest networks
+ discovery link routes   (when discovery.link_routes is on, and not --no-discovery)
+ discovery cloud CIDRs   (when discovery.cloud_metadata is on, and not --no-discovery)
- manifest exclude
- the reserved ranges and the remote's tailnet addresses
- the client's own connected subnets
- the local config exclude list and every --exclude flag
```

`tj connect` refuses when the result is empty. `tj describe <remote>` prints
the manifest, the discovery result, every exclusion, and the final list,
without starting a session.

Without `dns.servers`, the DNS mode uses the servers discovery finds on the
remote, minus the Tailscale MagicDNS addresses. Without `dns.domains`,
`--dns split` is not available; `--dns none` and `--dns all` still work.
The `all` mode needs no manifest at all: it sends every query to `dns.servers`,
or to the discovered resolvers when the manifest sets none.

## The transport section

A session runs its flows over a QUIC connection to the remote's tailnet
address, and falls back to the SSH channel when that connection does not
come up. The helper binds the first free UDP port of `transport.quic_ports`
on the tailnet address only, so the tailnet policy needs one UDP rule for the
range, for example `{"src": ["group:example"], "dst": ["tag:example"], "ip": ["udp:7443-7452"]}`.
Without the rule the session still works, on the SSH transport, and says so.

A rate is a number and a unit: `bps`, `kbps`, `mbps`, `gbps`, or `tbps`, in
bits per second, case-insensitive, with an optional space. Set
`transport.bandwidth` only when the path has a known capacity that the
remote's operator accepts as a fixed send rate: Brutal sends at that rate and
compensates loss, which is unfair on a shared relay. Without it, BBR measures
the path.

## Checks

Each entry in `checks` names a TCP endpoint. `tj doctor <remote>` dials
every check both from the remote, over the mux, and from the client
directly, and prints a pass or a fail line per check per side. A check with
no working path from the client is not necessarily a problem: it may become
reachable only once a session is up and routing its network.

## See also

* [`spec.md`](spec.md), section 5, contract C2: the schema this document
  documents, and C3: the discovery script that supplies the fields a
  manifest leaves absent.
* [`architecture.md`](architecture.md), "Session networks" and "Discovery
  script output": the computation and the discovery JSON shape.
