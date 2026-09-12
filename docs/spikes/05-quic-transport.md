# Spike 5: QUIC transport over the tailnet

## Goal

Confirm that a QUIC connection to a remote's tailnet address carries the tj data
plane, and settle the four decisions that chunk 1 of K8S-206 owes chunk 2: the
packet size, the helper size, the library, and the congestion controller.

The spike code is on the branch `spike/k8s-208-quic`. It is not merged, and
`go.mod` on `main` does not change in this chunk.

## Method

The client ran in a rootless podman container with a route to the tailnet range
through the host, because `tj connect` never runs on the client machine. A
throwaway Go server ran on each remote under `timeout`, deleted its own file at
start as the helper does, and served a QUIC listener, a raw TCP listener, and a
raw UDP probe responder on the tailnet address, on ports from the 7443 to 7452
range. Two remotes were used: the shared gateway, which has a direct Tailscale
path, and a private-subnet remote behind a NAT gateway, which Tailscale relays
through DERP with no direct path. Both are aarch64 with 2 vCPU and 910 MiB, and
both list `reno cubic` only as kernel congestion controllers. Every measurement
is from 2026-09-12.

## Result 1: the largest packet that crosses the path

A raw UDP probe sent payloads of the listed sizes and the remote answered with
the size it received. The do-not-fragment bit is the bit QUIC sets.

| Payload bytes | Rig container, no DF | Rig container, DF set | Client host, DF set |
| -- | -- | -- | -- |
| 1200 | 3/3 | 3/3 | 3/3 |
| 1232 | 3/3 | 3/3 | 3/3 |
| 1252 | 3/3 | 3/3 | 3/3 |
| 1253 | not run | 3/3 | 0/3, EMSGSIZE |
| 1272 | not run | 3/3 | 0/3, EMSGSIZE |
| 1280 | 3/3 | 3/3 | 0/3, EMSGSIZE |
| 1400 | 3/3 | 3/3 | 0/3, EMSGSIZE |

The result is the same on both remotes. The tailnet interface MTU is 1280. Over
IPv4 the 20-byte IP header and the 8-byte UDP header leave 1252 bytes of
payload, and the kernel refuses a larger datagram with DF set. Over IPv6 the
40-byte header would leave 1232.

The rig container is not a faithful path-MTU environment. Its user-mode NAT does
not carry the DF bit to the host, so the host fragments and every size up to
1400 is answered. Chunk 2 and chunk 3 must check packet size from a client that
owns its own route, not from the rig.

## Result 2: handshake and the packet size QUIC settles on

quic-go v0.62.0 has an `InitialPacketSize` default of 1280 bytes of payload,
which is 1308 bytes of IPv4 packet, which is above the tailnet MTU.

| Client packet size | Server packet size | Path | Result |
| -- | -- | -- | -- |
| 1280, library default | 1280, library default | shared gateway | no handshake in 20 s, 2 of 2 attempts |
| 1232 | 1280, library default | shared gateway | no handshake in 20 s |
| 1232 | 1232 | shared gateway | 34.5 ms, median of 5 |
| 1232 | 1232 | relayed remote | 25.4 ms, median of 5 |
| 1280, library default, from the host | 1232 | relayed remote | no handshake in 20 s |
| 1280, library default, from the rig | 1232 | relayed remote | 26.6 ms |

The library default fails in both directions, and the remote is the stricter
side: it has the tailnet MTU on its own interface, so it cannot send its own
handshake packets at all. The one success at 1280 is the rig, where the NAT
fragments.

A qlog trace recorded what path MTU discovery reaches.

| Discovery | Client | Largest datagram |
| -- | -- | -- |
| off | rig and host | 1232, no `mtu_updated` event |
| on | host, DF honoured | `mtu_updated` 1245 then 1252, and no higher |
| on | rig, DF dropped by the NAT | `mtu_updated` 1342, 1397, 1424, 1438 with `done` |

## Result 3: throughput on the direct path

Download of 256 MiB over one stream or one connection, median of 3 runs, from a
WiFi client to the shared gateway. The two QUIC rows are paired cycles, because
the path varies by a factor of 1.6 between runs. The spike 1 column is the
2026-09-11 measurement in `docs/architecture.md`.

| Path | Mbit/s, 2026-09-12 | Mbit/s, spike 1 |
| -- | -- | -- |
| Raw SSH channel, host | 54.5 | 53.7 |
| Raw TCP over the tailnet, host | 61.3 | 76.6 (tailnet direct) |
| Raw TCP over the tailnet, rig | 78.7 | not measured |
| QUIC 1232, library windows, rig | 102.4 | not measured |
| QUIC 1232, K8S-206 windows, rig | 100.2 | not measured |
| QUIC 1232, K8S-206 windows, host | 151.2 | not measured |
| tj IPv4 over the SSH channel | not measured | 51.8 |

QUIC is 1.9 times the same-day raw SSH channel from the rig and 2.8 times it
from the host. Across all 12 container runs of 256 MiB the range is 74.1 to
122.5 Mbit/s, so the medians carry that spread.

The K8S-206 receive windows have no measurable effect: 100.2 against 102.4
Mbit/s in the paired cycles, inside the run-to-run spread.

## Result 4: throughput on the relayed path

Download of 64 MiB, median of 3 runs, to the private-subnet remote behind a NAT
gateway, relayed through DERP. `tailscale ping` reported 23 ms to 56 ms and no
direct connection.

| Configuration | Mbit/s |
| -- | -- |
| QUIC 1232, library windows | 22.6 |
| QUIC 1232, K8S-206 windows | 22.6 |
| QUIC 1232, fork with Cubic | 22.6 |
| QUIC 1232, fork with BBR | 22.6 |
| Raw TCP over the tailnet | 22.7 |
| Raw SSH channel, host | 21.9 |

Every configuration lands between 22.0 and 22.8 Mbit/s, and the individual runs
vary by less than 3%. The relay behaved as a fixed-rate path with no measurable
loss on this day, so this measurement does not reproduce the collapse that
K8S-206 section 2 recorded. The controlled loss test in Result 5 is the
substitute, and chunk 3 needs it for the acceptance numbers.

A UDP socket bound to the remote's tailnet address received every probe that
arrived over DERP, and the reply reached the client. That answers the open
question in K8S-206 section 9 row 1.

## Result 5: BBR against Cubic against Brutal under loss

The server and the client both ran inside the rig container, and the loss was
applied with `tc qdisc add dev lo root netem loss 7% delay 30ms`. The loopback
interface was used because the container's outward interface is a user-mode NAT
whose qdisc does not shape the path to the host. Loopback egress is traversed
once per direction, so both directions get 7% loss and the round trip is 60 ms.
Loopback has no bandwidth limit, so the no-netem column is the cost of the loss
and not a path capacity.

| Controller | 7% loss, 30 ms delay | no netem |
| -- | -- | -- |
| Cubic, the library default | 1.7 Mbit/s | 6151 Mbit/s |
| BBR, the fork plus the copied hysteria BBR | 261.3 Mbit/s | 5651 Mbit/s |
| Brutal at a configured 100 Mbit/s | 95.3 Mbit/s | 98.0 Mbit/s |

BBR and Brutal are the median of 3 runs of 64 MiB. Cubic is the median of 3 runs
of 16 MiB, because a 64 MiB Cubic run takes 321.87 s; that single 64 MiB run
also returned 1.7 Mbit/s, so the smaller transfer does not flatter Cubic.

BBR carries 154 times what Cubic carries under this loss. Brutal holds 95% of
its configured rate under the same loss, which is what it is for, and it ignores
the path, which is why it stays opt-in.

## Result 6: helper size

Built with `CGO_ENABLED=0 -trimpath -ldflags "-s -w"`. The gate is 8 MiB.

| Build | linux/arm64 | linux/amd64 | packages |
| -- | -- | -- | -- |
| `cmd/tjhelper` as it is today | 2.25 MiB | 2.30 MiB | 82 |
| plus upstream quic-go and `crypto/tls` | 5.69 MiB | 6.19 MiB | 200 |
| plus the apernet fork, BBR, and Brutal | 6.38 MiB | 6.93 MiB | 263 |

Each variant links a QUIC listener, accepts a connection, and serves a stream,
so the linker keeps the code. The baseline row reproduces spike 4 exactly.

The upstream graph has no `encoding/json` and no `log/slog`. The fork graph has
`encoding/json`, `github.com/refraction-networking/utls`,
`github.com/andybalholm/brotli`, and `github.com/klauspost/compress`, because
the fork carries a TLS fingerprint feature that tj does not use. The build
section of `docs/architecture.md` says to keep `encoding/json` out of the helper
graph, so the fork needs that rule amended.

## Result 7: the apernet fork

Measured against `github.com/quic-go/quic-go` v0.62.0, released 2026-08-30.

* The fork's published semver tags, v0.48.2 up to v0.61.0, are the upstream tags
  that the GitHub fork inherited. Their `go.mod` declares
  `module github.com/quic-go/quic-go`, so `go get github.com/apernet/quic-go@v0.61.0`
  fails with "module declares its path as: github.com/quic-go/quic-go". The fork
  publishes no usable release tag.
* The usable code is on the branch `v0.61.0-mod-rename`, which is upstream
  v0.61.0 plus 7 commits: the module rename script, the module rename, the
  hysteria modifications, the chrome parrot feature and its fix, an exported
  `Conn.InitialPacketSize`, and `Transport.DisableGSO`. A consumer pins a
  pseudo-version from that branch. Hysteria pins
  `v0.61.1-0.20260806010916-184d081eef3e`, dated 2026-08-06, and this spike used
  that version.
* Lag: upstream v0.61.0 is dated 2026-07-21 and upstream v0.62.0 is dated
  2026-08-30. On 2026-09-12 the fork tracks v0.61.0, so it is one minor release
  and about six weeks behind.
* The hook is `func (c *Conn) SetCongestionControl(cc congestion.CongestionControl)`
  in `connection.go`, with the interface in the exported package
  `github.com/apernet/quic-go/congestion`. The fork also exports
  `Conn.InitialPacketSize()` to seed a replacement controller with the size QUIC
  starts at. Upstream v0.62.0 has no exported `congestion` package and no
  `SetCongestionControl`; its only controller is Cubic under
  `internal/congestion`.
* BBR and Brutal are in `github.com/apernet/hysteria` under
  `core/internal/congestion/{bbr,brutal,common}`. The `internal` path blocks an
  import from another module, confirmed by the module path rule, so tailjump has
  to copy them. The copy is 9 non-test files and 2762 lines: `bbr` 2489 lines,
  `brutal` 193 lines, and `common/pacer.go` 80 lines. They import
  `github.com/apernet/quic-go/congestion` and `.../monotime`, and one local
  package, so only the local import path changes.
* Licences: hysteria is MIT, "Copyright 2023 Toby". The fork is MIT, "Copyright
  (c) 2016 the quic-go authors & Google, Inc.". Both are compatible with
  tailjump's Apache 2.0, and each copied file keeps its MIT notice.

## Result 8: the tailnet policy for a UDP range

A grant entry in the `ip` field is `<proto>:<port>`, and the port may be a range
written with a hyphen, for example `"ip": ["tcp:443", "udp:7443-7452"]`. Source:
the Tailscale grants syntax reference at
https://tailscale.com/docs/reference/syntax/grants, and the ACL page at
https://tailscale.com/kb/1018/acls. The C4 example in K8S-206 is correct as
written.

Measured on 2026-09-12: a UDP datagram from this client reached a port in the
range on the tailnet address of both remotes, on the direct path and on the
relayed path, and the reply came back. The policy therefore already passes the
range. The policy document itself is not readable from here, so this is an
observation of the effect and not a reading of the rule.

## Decisions

1. **Packet size: 1232, path MTU discovery off. Confirmed, and it is mandatory,
   not an optimisation.** The library default of 1280 completes no handshake at
   all over the tailnet, in either direction. The measured ceiling is 1252 bytes
   of payload over IPv4, and path MTU discovery on a client that honours the DF
   bit settles at exactly 1252. Raising 1232 to 1252 would win 1.6% and would
   break the moment the transport dials an IPv6 tailnet address, where the
   ceiling is 1232. Keep discovery off: it gains 1.6% at best, and in a NAT
   environment it reports 1438 and relies on IP fragmentation.
2. **Helper size: pass.** The helper with upstream quic-go is 5.69 MiB for
   linux/arm64 and 6.19 MiB for linux/amd64. With the fork, BBR, and Brutal it is
   6.38 MiB and 6.93 MiB. The gate is 8 MiB, and the largest build uses 87% of
   it. There is no room for a second large dependency after this one.
3. **Library: the apernet fork, pinned at the pseudo-version from the
   `v0.61.0-mod-rename` branch.** Upstream has no way to replace the congestion
   controller, and the controller is the whole point of this transport: 261.3
   against 1.7 Mbit/s at 7% loss. The costs are real and must be accepted
   explicitly: no usable release tag, so the pin is a branch pseudo-version that
   Renovate cannot follow by tag; one minor release behind upstream; 0.69 MiB and
   63 packages more in the helper; and `encoding/json` in the helper graph
   through a TLS fingerprint feature tj does not use. If those costs are refused,
   the fallback is upstream with Cubic, and the acceptance criterion of 3 times
   the SSH transport under loss is then unreachable.
4. **Congestion control: BBR by default, Brutal opt-in. Confirmed.** BBR carries
   154 times what Cubic carries at 7% loss and 30 ms delay, and it costs nothing
   on a clean path. Brutal held 95.3 of its configured 100 Mbit/s under the same
   loss, so the Hysteria2 rule stands: a set `transport.bandwidth` means Brutal,
   an unset one means BBR.

### What K8S-206 section 5 got wrong

* The packet size row treats 1232 as the way to avoid a black hole. It is
  stronger than that. The library default cannot connect at all, so chunk 2 must
  set `InitialPacketSize` on the helper listener as well as on the client dial,
  and a missing setting on either side is a total failure and not a slow path.
* The library row leaves the choice open pending "an acceptable lag". The lag is
  acceptable at one minor release, but the row does not anticipate that the fork
  publishes no usable module tag. The pin is a branch pseudo-version, and the
  helper dependency rule of the same section has to give up `encoding/json` as
  well as allow quic-go.
* The receive windows of chunk 3, 8 MiB per stream and 20 MiB per connection,
  changed nothing here: 100.2 against 102.4 Mbit/s on the direct path and 22.6
  against 22.6 Mbit/s on the relay. Chunk 3 should treat them as unproven rather
  than as a starting point that only needs tuning.
* "Cubic is not acceptable on the relay path" holds under loss, but not because
  the relay is always lossy. On 2026-09-12 the relay carried every transport at
  22.6 Mbit/s with no measurable loss, including the SSH channel that collapsed
  in section 2. The relay's behaviour varies, so chunk 3 needs the `tc netem`
  rig for a repeatable acceptance measurement, and the live relay only as a
  sanity check.
* The 2 MiB UDP socket buffers were set on the spike server and made no
  measurable difference, and quic-go sets its own receive buffer. The spike did
  not isolate them, so they stay unmeasured.
