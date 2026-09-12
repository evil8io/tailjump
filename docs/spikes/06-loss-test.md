# Spike 6: the transports under loss

## Goal

Measure a tj session on the QUIC transport and on the SSH transport under
packet loss and delay, check the acceptance numbers of K8S-206 section 10,
compare the congestion controllers, and settle the receive windows, the UDP
socket buffers, the yamux window, and the packet size with measurements.

## Method

`test/e2e/loss.sh` ran every measurement from the rootless podman rig on a
WiFi client on 2026-09-12. The bench server from `test/e2e/loss/bench` ran on
the remote under `timeout`, bound to the remote's VPC addresses, and deleted
its own file at start. For each transport the job connected with
`--dns none --transport <quic|ssh>`, then added `tc netem delay 30ms loss 7%`
on the rig's outward interface for egress and on an `ifb` device for ingress,
so both directions lose 7% and the round trip gains 60 ms. The shaping went on
after the session was up, so the numbers describe the data plane and not the
SSH bootstrap. The job then measured 20 TCP connects through the session to
the VPC resolver on port 53 with an 8 s limit each, 20 UDP DNS queries to the
same resolver with a 2 s limit, and downloads of 256 MiB from the bench server
through the session, median of 3, with a 60 s time box per run; a run that hit
the box reports the rate of the bytes it received. The clean-path runs used the
same job with no shaping.

Two remotes served: the shared gateway, which has a direct Tailscale path, and
a private-subnet remote behind a NAT gateway, which Tailscale relays through
DERP with no direct path. Both are aarch64 nodes with 2 vCPU. The relayed
remote ran the clean-path job only, once, because it is small.

The A/B runs of the windows and the yamux window used 128 MiB over IPv4 only,
with the default and the variant built and run back to back, so the two sides
of each pair share the same path conditions as closely as a WiFi client
allows. The rule for a change was a gain of at least 10% in the median of 3.

## Result 1: both transports, 7% loss and 30 ms delay in each direction

Through a tj session to the shared gateway, 2026-09-12, with the shipped
configuration, which includes the receive windows of Result 4.

| Measure | QUIC, BBR | SSH transport |
| -- | -- | -- |
| Connect wall time | 2.8 s | 3.3 s |
| TCP connect p50 | 91.1 ms | 155.0 ms |
| TCP connect p95 | 94.0 ms | 1211.0 ms |
| TCP connect max, and failed of 20 | 270.0 ms, 0 | 3416.7 ms, 0 |
| UDP DNS answered within 2 s | 20 of 20 | 20 of 20 |
| Bulk 256 MiB over IPv4, median of 3 | 137.9 Mbit/s (135.2, 140.7, 137.9) | 26.1 Mbit/s (30.6, 23.9, 26.1), every run hit the 60 s box |
| Bulk 256 MiB over IPv6, median of 3 | 133.9 Mbit/s (131.2, 133.9, 134.6) | 34.6 Mbit/s (35.5, 34.6, 28.2), every run hit the 60 s box |

An earlier run of the same job with the library receive windows measured 108.2
and 106.8 Mbit/s on QUIC, 31.9 and 35.8 Mbit/s on the SSH transport, and a TCP
connect p95 of 92.1 ms against 1545.3 ms.

The QUIC transport keeps every connect inside one round trip plus the netem
delay, because a lost packet stalls one stream. On the SSH transport a lost
segment stalls every flow: the slowest connect took 3.4 s and the p95 is 1.2 s.
The 20 DNS queries ran one at a time with nothing else on the session, and both
transports answered all of them; the 1-in-6 baseline of K8S-206 section 2 was
measured on a session whose one TCP connection had already collapsed under a
bulk transfer, and the rig does not reproduce that state with idle queries.

## Result 2: both transports on the clean path

The same job with no shaping, the same day, the shipped configuration. The
spike 1 column is the 2026-09-11 measurement of the SSH-transport tj in
`docs/architecture.md`.

| Measure | QUIC, BBR | SSH transport | Spike 1, SSH transport |
| -- | -- | -- | -- |
| Connect wall time | 2.3 s | 2.3 s | not measured |
| TCP connect p50 / p95 | 30.7 / 31.4 ms | 30.9 / 34.5 ms | not measured |
| UDP DNS answered within 2 s | 20 of 20 | 20 of 20 | not measured |
| Bulk 256 MiB over IPv4, median of 3 | 231.7 Mbit/s (226.9, 234.9, 231.7) | 81.1 Mbit/s (79.3, 82.0, 81.1) | 51.8 Mbit/s |
| Bulk 256 MiB over IPv6, median of 3 | 207.8 Mbit/s (211.5, 207.8, 185.6) | 70.8 Mbit/s (70.8, 67.7, 73.7) | 65.9 Mbit/s |

An earlier run of the same job with the library receive windows measured 220.5
and 204.2 Mbit/s on QUIC and 56.8 and 56.0 Mbit/s on the SSH transport. The
SSH transport is capped by the one SSH channel, and that channel varies with
the WiFi path: 49 to 64 Mbit/s across the spike 1 runs, 54.5 Mbit/s in spike
5, and 56 to 81 Mbit/s in the two runs here. The QUIC transport is 2.9 times
the SSH transport in the same run over both address families.

## Result 3: the congestion controllers through tj at 7% loss and 30 ms delay

QUIC transport, the shared gateway. Brutal ran from a temporary manifest with
`transport.bandwidth` of 20 mbps up and 50 mbps down, both below the clean-path
capacity, and the manifest was removed again. Cubic ran through the measurement
knob `TJ_QUIC_CONTROLLER=cubic`.

| Controller | Bulk IPv4 | Bulk IPv6 | TCP connect p50 / p95 | DNS within 2 s |
| -- | -- | -- | -- | -- |
| BBR, the default | 108.2 Mbit/s | 106.8 Mbit/s | 91.3 / 92.1 ms | 20 of 20 |
| Brutal at 50 mbps down | 48.3 Mbit/s | 48.5 Mbit/s | 93.0 / 240.2 ms | 20 of 20 |
| Cubic, the library default | 0.5 Mbit/s | 0.6 Mbit/s | 92.1 / 264.7 ms | 20 of 20 |

Brutal holds 97% of its configured rate under the loss, and BBR carries 216
times what Cubic carries. Every Cubic download hit the 60 s time box. The
BBR row and the spike 5 loopback run agree on the order of magnitude: 154
times there, 216 times here.

## Result 4: the receive windows

QUIC transport, 128 MiB over IPv4, median of 3, paired runs of the library
defaults against 8 MiB per stream and 20 MiB per connection.

| Path | Transfer | Library defaults | 8 MiB and 20 MiB | Change |
| -- | -- | -- | -- | -- |
| 7% loss, 30 ms delay, pair 1 | 128 MiB | 90.4 Mbit/s (51.4, 116.2, 90.4) | 140.2 Mbit/s (63.3, 140.2, 145.4) | +55% |
| 7% loss, 30 ms delay, pair 2 | 128 MiB | 103.0 Mbit/s (60.2, 106.2, 103.0) | 132.4 Mbit/s (60.6, 134.4, 132.4) | +29% |
| 7% loss, 30 ms delay, pair 3 | 256 MiB | 112.2 Mbit/s (71.4, 116.6, 112.2) | 137.3 Mbit/s (103.1, 140.6, 137.3) | +22% |
| clean, pair 1 | 128 MiB | 267.7 Mbit/s (267.7, 274.8, 266.8) | 292.9 Mbit/s (275.4, 301.8, 292.9) | +9% |
| clean, pair 3 | 256 MiB | 219.4 Mbit/s (224.3, 209.2, 219.4) | 232.6 Mbit/s (230.7, 236.4, 232.6) | +6% |

The library defaults are a 512 KiB initial stream window that grows to 6 MiB
and a 15 MiB connection window. The first run of every triple is the lowest,
because it starts on a fresh session while BBR still measures the path. The
gain under loss is above the 10% rule in all three pairs, and the clean path
does not regress, so the windows ship: both sides now start at 8 MiB per
stream and 20 MiB per connection, as Hysteria2 does. Spike 5 measured no gain
from the same windows on a raw QUIC stream; through tj the stream feeds the
netstack relay, and the measured difference is in that path.

## Result 5: the UDP socket buffers

Measured on a live session with `ss -aunm` on both sides.

| Side | Runs as | Kernel cap `net.core.rmem_max` | Receive buffer | Send buffer |
| -- | -- | -- | -- | -- |
| Helper on the remote | root | 4 MiB | 8 MiB | 8 MiB |
| Client in the rig | root | 4 MiB | 8 MiB | 8 MiB |

The fork asks the kernel for 8 MiB on both sockets at start, and when the
first attempt stays below the cap it retries with `SO_RCVBUFFORCE`, which a
root process may use above the cap. Both sides run as root: the session runs
in a root unit, and the helper runs as the SSH user, which is root on the test
remotes. The result is 8 MiB on both sides, which is 4 times the 2 MiB of the
story, so an explicit 2 MiB setting is a downgrade that the fork raises again
at start. There is no A/B to run, and no change ships. The fork ignores a
buffer error silently, so a non-root helper on a remote with the 4 MiB cap
gets 4 MiB and logs nothing; upstream quic-go logs one warning in that case.
GSO is on: the fork's `Transport.DisableGSO` defaults to off, the probe needs
kernel 5 or later and `UDP_SEGMENT`, and the rig kernel is 7.0 and the remote
kernel is 6.18.

## Result 6: the yamux window on the SSH transport

SSH transport, 128 MiB over IPv4, median of 3, paired runs of the 4 MiB window
against 512 KiB.

| Path | 4 MiB, the current value | 512 KiB | Change |
| -- | -- | -- | -- |
| 7% loss, 30 ms delay | 34.2 Mbit/s | 8.2 Mbit/s | -76% |
| clean | 55.5 Mbit/s | 46.2 Mbit/s | -17% |

The 4 MiB window stays. Every 512 KiB run under loss hit the 60 s time box, and the clean-path loss of 17% matches spike 1, where the 256 KiB default cost 36%. The head-of-line blocking of the one channel is not a window problem. The cap of 8 flows per SSH connection in the chunk 3 row of
K8S-206 is not implemented: it needs a pool of SSH connections, which section
3 of the story lists as a non-goal, because the SSH transport is the fallback
only.

## Result 7: the packet size

No tuning. Spike 5 measured a ceiling of 1252 bytes over IPv4, which is 1.6%
above the 1232 in use, and a ceiling of 1232 over IPv6, and the rig's NAT
does not carry the DF bit, so the rig cannot measure it. The value stays 1232
on both sides.

## Result 8: the relayed remote

The clean-path job from the rig against a private-subnet remote behind a NAT
gateway, which Tailscale relays through DERP with no direct path. One
campaign, 64 MiB over IPv4, median of 3, no shaping, both transports.

| Measure | QUIC, BBR | SSH transport |
| -- | -- | -- |
| Connect wall time | 6.6 s | 4.1 s |
| TCP connect p50 / p95 / failed of 20 | 22.5 / 30.1 ms / 0 | 23.0 / 32.5 ms / 0 |
| UDP DNS answered within 2 s | 20 of 20 | 20 of 20 |
| Bulk 64 MiB over IPv4, median of 3 | 22.2 (22.3, 22.1, 22.2) Mbit/s | 22.3 (21.7, 22.4, 22.3) Mbit/s |

The relay carried both transports at the same rate with no measurable loss, as spike 5 found on the same day. The collapse of K8S-206 section 2 needs loss on the relay, which the relay did not show on 2026-09-12, so the netem runs above are the acceptance measurement and this run is the sanity check: the QUIC transport comes up over DERP, every connect and query succeeds, and the throughput equals the path.

## Acceptance, K8S-206 section 10

The criteria apply to the default transport, which is QUIC with BBR, in the
shipped configuration. The SSH transport is the fallback and is the comparison.

| Criterion | Target | QUIC, BBR | Verdict |
| -- | -- | -- | -- |
| TCP connect p95 through the session, 7% loss and 30 ms delay | under 1 s | 94.0 ms | pass |
| UDP DNS answered within 2 s, 7% loss and 30 ms delay | at least 95% | 100%, 20 of 20 | pass |
| Bulk 256 MiB over IPv4 under the same loss, against the SSH transport | at least 3 times | 137.9 against 26.1 Mbit/s, 5.3 times | pass |
| Bulk 256 MiB over IPv6 under the same loss, against the SSH transport | at least 3 times | 133.9 against 34.6 Mbit/s, 3.9 times | pass |
| Bulk 256 MiB over IPv4, clean, against spike 1 | at least 90% of 51.8 Mbit/s | 231.7 Mbit/s, 447% | pass |
| Bulk 256 MiB over IPv6, clean, against spike 1 | at least 90% of 65.9 Mbit/s | 207.8 Mbit/s, 315% | pass |

With the library receive windows the IPv6 bulk ratio was 2.98 times, 106.8
against 35.8 Mbit/s, which is under the line; the shipped windows lift it to
3.9 times.

The functional criteria of section 10 are covered by `test/e2e/run.sh` and
`test/e2e/crash.sh` since chunk 2: the fallback within the budget with the
warning, no port and no file on the remote after `disconnect`, the helper
under 8 MiB at 6.44 MiB for linux/arm64 and 6.96 MiB for linux/amd64, the
transport in `tj status` and `tj doctor`, and the `darwin/arm64` build in CI.
The final run of `run.sh` on this branch is in the pull request.

## Decisions

1. **Congestion control: BBR by default, Brutal opt-in. Confirmed through tj.**
   BBR carries 216 times what Cubic carries at 7% loss and 30 ms delay, and
   Brutal holds 97% of its configured rate. Nothing changes.
2. **Receive windows: 8 MiB per stream and 20 MiB per connection ship, on both sides.** Three paired runs under 7% loss gained 55%, 29%, and 22%, and the clean path gained 6% to 9%.
3. **UDP socket buffers: the library behaviour stays.** Both sides already run
   with 8 MiB buffers through `SO_RCVBUFFORCE`, above the 2 MiB of the story
   and above the kernel cap. No code change.
4. **Yamux window: 4 MiB stays.** The 512 KiB window lost 76% under loss and 17% on the clean path.
5. **Packet size: 1232 on both sides stays.** The measured gain of a larger
   size is 1.6% over IPv4 and zero over IPv6, and the rig cannot check it.
6. **Flow cap on the SSH transport: not implemented.** It needs the SSH
   connection pool that K8S-206 section 3 excludes.
7. **The `TJ_QUIC_CONTROLLER=cubic` knob ships as a measurement knob only.**
   It has no place in the config, the manifest, or the README.
