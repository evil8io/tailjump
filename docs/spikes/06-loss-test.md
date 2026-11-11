# Spike 6: the transports under loss

## Goal

These are the goals of the spike:

1. Measure a tj session on the QUIC transport and on the SSH transport under packet loss and delay.
2. Check the acceptance numbers of section 10 of the v2 transport spec.
3. Compare the congestion controllers.
4. Decide the receive windows, the UDP socket buffers, the yamux window, and the packet size with measurements.

## Method

`test/e2e/loss.sh` ran every measurement from the rootless podman rig on a
WiFi client on 2026-09-12. The bench server from `test/e2e/loss/bench` ran on
the remote under `timeout`, bound to the remote's VPC addresses, and deleted
its own file at start.

For each transport, the job connected with `--dns none --transport <quic|ssh>`. Then the job added `tc netem delay 30ms loss 7%` on the rig's outward interface for egress and on an `ifb` device for ingress. Thus the loss is 7% in each direction. The round trip is 60 ms longer. The job added the shaping after the session was up. Thus the numbers apply to the data plane and not to the SSH bootstrap.

The job then did these measurements through the session:

* 20 TCP connects to the VPC resolver on port 53, with a limit of 8 s for each connect.
* 20 UDP DNS queries to the same resolver, with a limit of 2 s.
* Downloads of 256 MiB from the bench server, with a time box of 60 s for each run. The result is the median of 3 runs.

When a run hit the time box, the result of the run is the rate of the received bytes. For the clean-path runs, the job did not add the shaping.

The job ran against two remotes. The first is the shared gateway, which has a direct Tailscale path. The second is a private-subnet remote behind a NAT gateway. It does not have a direct path. Tailscale relays it through DERP. Both are aarch64 nodes with 2 vCPU. The job ran against the relayed remote once, on the clean path only, because the remote is small.

In the A/B runs of the windows and the yamux window, the transfer was 128 MiB over IPv4 only. For each pair, the default and the variant were built and run back to back. Thus the path conditions of the two sides of each pair are as close as possible on a WiFi client. The rule for a change was a gain of at least 10% in the median of 3.

## Result 1: both transports, 7% loss and 30 ms delay in each direction

The numbers are from a tj session to the shared gateway on 2026-09-12. The session ran with the shipped configuration, which includes the receive windows of Result 4.

| Measure | QUIC, BBR | SSH transport |
| -- | -- | -- |
| Connect wall time | 2.8 s | 3.3 s |
| TCP connect p50 | 91.1 ms | 155.0 ms |
| TCP connect p95 | 94.0 ms | 1211.0 ms |
| TCP connect max, and failed of 20 | 270.0 ms, 0 | 3416.7 ms, 0 |
| UDP DNS answered within 2 s | 20 of 20 | 20 of 20 |
| Bulk 256 MiB over IPv4, median of 3 | 137.9 Mbit/s (135.2, 140.7, 137.9) | 26.1 Mbit/s (30.6, 23.9, 26.1), every run hit the 60 s box |
| Bulk 256 MiB over IPv6, median of 3 | 133.9 Mbit/s (131.2, 133.9, 134.6) | 34.6 Mbit/s (35.5, 34.6, 28.2), every run hit the 60 s box |

An earlier run of the same job ran with the library receive windows. In that run, the job measured 108.2 and 106.8 Mbit/s on QUIC and 31.9 and 35.8 Mbit/s on the SSH transport. The TCP connect p95 was 92.1 ms against 1545.3 ms.

The QUIC transport keeps every connect inside one round trip plus the netem
delay, because only one stream stalls after a lost packet. On the SSH transport, every flow stalls after a lost segment. The slowest connect took 3.4 s. The p95 is 1.2 s.

The job sent the 20 DNS queries one at a time. The session was idle apart from the queries. On both transports, all 20 queries were answered. The 1-in-6 baseline in section 2 of the v2 transport spec was measured on another session. Before that measurement, the one TCP connection of that session collapsed under a bulk transfer. The rig does not reproduce that state with idle queries.

## Result 2: both transports on the clean path

The job ran on the same day with the shipped configuration, but it did not add the shaping. The
spike 1 column is the 2026-09-11 measurement of the SSH-transport tj in
`docs/architecture.md`.

| Measure | QUIC, BBR | SSH transport | Spike 1, SSH transport |
| -- | -- | -- | -- |
| Connect wall time | 2.3 s | 2.3 s | not measured |
| TCP connect p50 / p95 | 30.7 / 31.4 ms | 30.9 / 34.5 ms | not measured |
| UDP DNS answered within 2 s | 20 of 20 | 20 of 20 | not measured |
| Bulk 256 MiB over IPv4, median of 3 | 231.7 Mbit/s (226.9, 234.9, 231.7) | 81.1 Mbit/s (79.3, 82.0, 81.1) | 51.8 Mbit/s |
| Bulk 256 MiB over IPv6, median of 3 | 207.8 Mbit/s (211.5, 207.8, 185.6) | 70.8 Mbit/s (70.8, 67.7, 73.7) | 65.9 Mbit/s |

An earlier run of the same job ran with the library receive windows. In that run, the job measured 220.5 and 204.2 Mbit/s on QUIC and 56.8 and 56.0 Mbit/s on the SSH transport.

The rate of the SSH transport is capped by the one SSH channel. The rate of that channel changes with the WiFi path. It was 49 to 64 Mbit/s across the spike 1 runs and 54.5 Mbit/s in spike 5. In the two runs here, it was 56 to 81 Mbit/s. The rate of the QUIC transport is 2.9 times the rate of the SSH transport in the same run, over both address families.

## Result 3: the congestion controllers through tj at 7% loss and 30 ms delay

The transport is QUIC. The remote is the shared gateway. Brutal ran from a temporary manifest with `transport.bandwidth` of 20 mbps up and 50 mbps down, both below the clean-path capacity. Then the manifest was removed. Cubic ran through the measurement
knob `TJ_QUIC_CONTROLLER=cubic`.

| Controller | Bulk IPv4 | Bulk IPv6 | TCP connect p50 / p95 | DNS within 2 s |
| -- | -- | -- | -- | -- |
| BBR, the default | 108.2 Mbit/s | 106.8 Mbit/s | 91.3 / 92.1 ms | 20 of 20 |
| Brutal at 50 mbps down | 48.3 Mbit/s | 48.5 Mbit/s | 93.0 / 240.2 ms | 20 of 20 |
| Cubic, the library default | 0.5 Mbit/s | 0.6 Mbit/s | 92.1 / 264.7 ms | 20 of 20 |

Under the loss, Brutal reaches 97% of its configured rate. With BBR, the rate is 216 times the rate with Cubic. Every Cubic download hit the 60 s time box. The BBR ratio is of the same order of magnitude as in the spike 5 loopback run: 154 times there, 216 times here.

## Result 4: the receive windows

These are paired runs on the QUIC transport, with 128 MiB over IPv4 and the median of 3. In each pair, one run uses the library defaults, and the other run uses 8 MiB per stream and 20 MiB per connection.

| Path | Transfer | Library defaults | 8 MiB and 20 MiB | Change |
| -- | -- | -- | -- | -- |
| 7% loss, 30 ms delay, pair 1 | 128 MiB | 90.4 Mbit/s (51.4, 116.2, 90.4) | 140.2 Mbit/s (63.3, 140.2, 145.4) | +55% |
| 7% loss, 30 ms delay, pair 2 | 128 MiB | 103.0 Mbit/s (60.2, 106.2, 103.0) | 132.4 Mbit/s (60.6, 134.4, 132.4) | +29% |
| 7% loss, 30 ms delay, pair 3 | 256 MiB | 112.2 Mbit/s (71.4, 116.6, 112.2) | 137.3 Mbit/s (103.1, 140.6, 137.3) | +22% |
| clean, pair 1 | 128 MiB | 267.7 Mbit/s (267.7, 274.8, 266.8) | 292.9 Mbit/s (275.4, 301.8, 292.9) | +9% |
| clean, pair 3 | 256 MiB | 219.4 Mbit/s (224.3, 209.2, 219.4) | 232.6 Mbit/s (230.7, 236.4, 232.6) | +6% |

The library defaults are a 512 KiB initial stream window that grows to 6 MiB
and a 15 MiB connection window. The first run of every triple is the lowest,
because it starts on a fresh session while BBR still measures the path.

In all three pairs, the gain under loss is above the 10% rule. The rate on the clean path does not decrease. Thus the windows are in the shipped configuration: both sides now start at 8 MiB per stream and 20 MiB per connection, as Hysteria2 does. In spike 5, the rate on a raw QUIC stream did not increase with the same windows. Through tj, the data of the stream goes into the netstack relay. The measured difference is in that path.

## Result 5: the UDP socket buffers

The values are from `ss -aunm` on both sides of a live session.

| Side | Runs as | Kernel cap `net.core.rmem_max` | Receive buffer | Send buffer |
| -- | -- | -- | -- | -- |
| Helper on the remote | root | 4 MiB | 8 MiB | 8 MiB |
| Client in the rig | root | 4 MiB | 8 MiB | 8 MiB |

At start, the fork asks the kernel for 8 MiB on both sockets. When the result of the first attempt is below the cap, the fork retries with `SO_RCVBUFFORCE`. A root process can use that option above the cap. Both sides run as root. The session runs in a root unit. The helper runs as the SSH user, which is root on the test remotes.

The result is 8 MiB on both sides, which is 4 times the 2 MiB of the story. An explicit 2 MiB setting is thus a downgrade that the fork raises again at start. An A/B run is not necessary. The code does not change.

The fork ignores a buffer error and does not log it. Thus a non-root helper on a remote with the 4 MiB cap gets 4 MiB and does not log a message. Upstream quic-go logs one warning in that case.

GSO is on, for these reasons:

* The default of `Transport.DisableGSO` in the fork is off.
* The probe needs kernel 5 or later and `UDP_SEGMENT`.
* The rig kernel is 7.0, and the remote kernel is 6.18.

## Result 6: the yamux window on the SSH transport

SSH transport, 128 MiB over IPv4, median of 3, paired runs of the 4 MiB window
against 512 KiB.

| Path | 4 MiB, the current value | 512 KiB | Change |
| -- | -- | -- | -- |
| 7% loss, 30 ms delay | 34.2 Mbit/s | 8.2 Mbit/s | -76% |
| clean | 55.5 Mbit/s | 46.2 Mbit/s | -17% |

The 4 MiB window stays. Every 512 KiB run under loss hit the 60 s time box. The clean-path loss of 17% is consistent with spike 1. In spike 1, the rate with the 256 KiB default was 36% lower. The head-of-line blocking of the one channel is not a window problem.

The cap of 8 flows per SSH connection in the chunk 3 row of the v2 transport spec is not implemented. The cap needs a pool of SSH connections. That pool is a non-goal in section 3 of the story, because the SSH transport is the fallback only.

## Result 7: the packet size

The packet size was not tuned. In spike 5, the measured ceiling was 1252 bytes over IPv4, which is 1.6% above the 1232 in use. Over IPv6, the measured ceiling was 1232. The rig's NAT does not keep the DF bit, so the ceiling cannot be measured in the rig. The value stays 1232
on both sides.

## Result 8: the relayed remote

The job ran the clean-path measurements from the rig against a private-subnet remote behind a NAT gateway. That remote does not have a direct path. Tailscale relays it through DERP. The job ran one campaign on both transports, with 64 MiB over IPv4 and the median of 3. The job did not add the shaping.

| Measure | QUIC, BBR | SSH transport |
| -- | -- | -- |
| Connect wall time | 6.6 s | 4.1 s |
| TCP connect p50 / p95 / failed of 20 | 22.5 / 30.1 ms / 0 | 23.0 / 32.5 ms / 0 |
| UDP DNS answered within 2 s | 20 of 20 | 20 of 20 |
| Bulk 64 MiB over IPv4, median of 3 | 22.2 (22.3, 22.1, 22.2) Mbit/s | 22.3 (21.7, 22.4, 22.3) Mbit/s |

Through the relay, both transports reached the same rate. The loss was not measurable. The result is the same as in spike 5 on the same day.

The collapse in section 2 of the v2 transport spec needs loss on the relay. On 2026-09-12, that condition was not present. Thus the netem runs above are the acceptance measurement. This run is the sanity check for these results:

* The QUIC transport comes up over DERP.
* Every connect and every query succeeds.
* The throughput equals the capacity of the path.

## Acceptance, section 10 of the v2 transport spec

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

Since chunk 2, these functional criteria of section 10 are covered by `test/e2e/run.sh` and `test/e2e/crash.sh`:

* The fallback within the budget, with the warning.
* No port and no file on the remote after `disconnect`.
* The helper under 8 MiB, at 6.44 MiB for linux/arm64 and 6.96 MiB for linux/amd64.
* The transport in `tj status` and `tj doctor`.
* The `darwin/arm64` build in CI.

The final run of `run.sh` on this branch is in the pull request.

## Decisions

1. **Congestion control: BBR by default, Brutal opt-in. Confirmed through tj.**
   At 7% loss and 30 ms delay, the rate with BBR is 216 times the rate with Cubic.
   Brutal reaches 97% of its configured rate. Nothing changes.
2. **Receive windows: 8 MiB per stream and 20 MiB per connection ship, on both sides.** Under 7% loss, the gain in three paired runs was 55%, 29%, and 22%. On the clean path, the gain was 6% to 9%.
3. **UDP socket buffers: the library behaviour stays.** Both sides already run
   with 8 MiB buffers through `SO_RCVBUFFORCE`, above the 2 MiB of the story
   and above the kernel cap. The code does not change.
4. **Yamux window: 4 MiB stays.** With the 512 KiB window, the rate was 76% lower under loss and 17% lower on the clean path.
5. **Packet size: 1232 on both sides stays.** The measured gain of a larger
   size is 1.6% over IPv4 and zero over IPv6. The gain cannot be checked in the rig.
6. **Flow cap on the SSH transport: not implemented.** It needs the SSH
   connection pool, which is excluded in section 3 of the v2 transport spec.
7. **The `TJ_QUIC_CONTROLLER=cubic` knob ships as a measurement knob only.**
   It is not in the config, the manifest, or the README.
