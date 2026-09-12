# Spike 10: one SSH connection per protocol on the SSH transport

## Goal

Measure what the lanes of K8S-217 change on the SSH transport: the latency
of a DNS query, a TCP DNS query, and an ICMP echo during a bulk download,
the bulk rate, and the connect time, on the direct path and on the relayed
path, in paired cycles with and without the lanes. Spike 9 measured the
transport without lanes: a DNS query under load took up to 795 ms on the
relayed path and up to 355 ms on the direct path, because one TCP
connection with a 4 MiB yamux window carried every flow.

## Method

The client is the laptop of spike 9 on the same WiFi link, and the two
remotes are the two aarch64 remotes of spike 9: the direct remote in a public
dual-stack subnet, which the client reaches over IPv6 without a relay, and
the relayed remote in a private IPv4-only subnet with the relay forced
through `TS_DEBUG_ALWAYS_USE_DERP=true`. `test/e2e/compare.sh` ran every
session on 2026-09-12 between 23:11 and 23:24 with `TJ_TEST_SESSIONS=ssh`,
one cycle per invocation, so that one cycle is the pair of a session with
`TJ_SSH_LANES=1`, the single lane, followed by a session with the lanes. Two
cycles per remote, the relayed remote first. tj was the build of the story
branch, `v1.4.0-5-gdc9e2ef`, on both the client and the root copy, with
`tj connect <remote> --dns all --transport ssh`. The lanes session opens the
four lanes `tcp,udp,icmp,dns`.

Per session, in this order:

1. A download of 60 s from the bench server over IPv4, with probes every 5 s
   during the download: one DNS query over UDP, one over TCP with
   `dig +tcp`, one ICMP echo to the remote's VPC address, one TCP connect to
   the remote's tailnet SSH port, which does not go through the session and
   measures the shared path next to the download, one TCP connect to the VPC
   resolver through the session, and one `tailscale ping`. The lanes route
   the queries over the dns lane, the echo over the icmp lane, and the
   download over the tcp lane.
2. 20 TCP connects to the resolver's port 53 and 10 DNS queries, idle.
3. One lookup of the remote's own private name through the system resolver.
4. On the direct remote, a download of 30 s over IPv6.

The idle round-trip time is 28 to 31 ms on the relayed path and 24 to 27 ms
on the direct path, from the idle queries after each download. The connect
time is the time from the `connect` line of the log to the first status
line, which includes up to one second of readiness polling.

Every probe in every session got an answer, and both remotes had no file,
listener, or process left after each session.

## Result 1: latency under load, relayed path

Minimum to maximum over the 8 to 9 probes during the download, single lane /
lanes, cycle 1 and cycle 2.

| Probe | Single lane, cycle 1 | Lanes, cycle 1 | Single lane, cycle 2 | Lanes, cycle 2 |
|-------|----------------------|----------------|----------------------|----------------|
| DNS over UDP | 30 to 466 ms | 29 to 133 ms | 28 to 590 ms | 29 to 146 ms |
| DNS over TCP | 321 to 384 ms | 27 to 363 ms | 272 to 373 ms | 28 to 148 ms |
| ICMP echo | 410 to 448 ms | 28 to 134 ms | 381 to 427 ms | 55 to 147 ms |
| TCP connect through the session | 327 to 508 ms | 28 to 138 ms | 306 to 386 ms | 55 to 151 ms |
| TCP connect next to the session | 62 to 125 ms | 27 to 134 ms | 37 to 122 ms | 52 to 147 ms |

The lanes take the DNS query under load from a maximum of 466 and 590 ms to
133 and 146 ms, and the echo from 448 and 427 ms to 134 and 147 ms. In the
lane sessions the four probes through the session and the connect next to
the session move together, within a few milliseconds in every probe line,
so the remaining latency is the queue of the relay, which every connection
through that relay shares and which no lane can remove. The maxima of the
DNS query and the echo equal the maximum of the connect next to the session
in the same cycle, 134 and 147 ms, which is the criterion of acceptance 1
for the relayed path. One TCP query of 363 ms in cycle 1 is above that line;
the other 16 TCP queries under load in the two lane sessions stay within it.
The relay queue was shallower than in the pre-measurement of the story,
which saw up to 487 ms on the connect next to the download: the maximum
here was 147 ms.

The 3 times idle criterion of acceptance 2, 90 ms, does not hold on the
relayed path, for the reason the story gives in its section 1: the relay
queue is outside the SSH connection.

## Result 2: latency under load, direct path

Minimum to maximum over the 10 to 12 probes during the download.

| Probe | Single lane, cycle 1 | Lanes, cycle 1 | Single lane, cycle 2 | Lanes, cycle 2 |
|-------|----------------------|----------------|----------------------|----------------|
| DNS over UDP | 28 to 298 ms | 24 to 31 ms | 25 to 345 ms | 25 to 31 ms |
| DNS over TCP | 35 to 251 ms | 24 to 28 ms | 33 to 487 ms | 24 to 27 ms |
| ICMP echo | 70 to 261 ms | 24 to 29 ms | 90 to 437 ms | 24 to 29 ms |
| TCP connect through the session | 25 to 231 ms | 24 to 28 ms | 59 to 372 ms | 25 to 31 ms |
| TCP connect next to the session | 23 to 35 ms | 24 to 27 ms | 23 to 33 ms | 23 to 26 ms |

On the direct path the lanes keep every probe at the idle round-trip time:
the DNS query at most 31 ms over UDP and 28 ms over TCP, and the echo at
most 29 ms, against the bound of 75 ms of acceptance 1 and 2, and against
298 and 345 ms without lanes in the same cycles and 355 ms in spike 9. The
connect next to the session stays at idle in every session, so the direct
path has no shared queue, and the lanes remove the whole queue.

## Result 3: latency idle, bulk rate, and connect time

| Number | Relayed, single / lanes, cycle 1 | Relayed, single / lanes, cycle 2 | Direct, single / lanes, cycle 1 | Direct, single / lanes, cycle 2 |
|--------|----------------------------------|----------------------------------|---------------------------------|---------------------------------|
| DNS idle, min to max | 28 to 31 / 28 to 46 ms | 28 to 37 / 27 to 34 ms | 24 to 27 / 24 to 28 ms | 24 to 26 / 25 to 27 ms |
| Connect idle, p50 / p95 | 29.0 / 29.8, 28.2 / 52.7 ms | 29.2 / 30.4, 28.8 / 30.5 ms | 24.9 / 27.2, 24.8 / 26.2 ms | 25.1 / 27.2, 24.9 / 27.1 ms |
| Bulk IPv4, 60 s | 22.4 / 21.8 Mbit/s | 22.3 / 22.2 Mbit/s | 131.9 / 101.8 Mbit/s | 121.6 / 131.6 Mbit/s |
| Bulk IPv6, 30 s | none | none | 160.1 / 107.9 Mbit/s | 112.7 / 144.4 Mbit/s |
| Connect time | 3 / 4 s | 4 / 4 s | 1 / 2 s | 1 / 2 s |

Idle, the lanes change nothing: every query and connect is at the round-trip
time, and the one p95 of 52.7 ms is the relay queue draining right after the
download. The relayed bulk rate is at the relay ceiling with and without
lanes, within 3%. On the direct path the rate differs by 23% below in cycle
1 and 8% above in cycle 2 over IPv4, and by 33% below and 28% above over
IPv6; the two single-lane cycles differ by 30% between themselves over IPv6,
160.1 against 112.7 Mbit/s, so the WiFi variance is above the 10% bound of
acceptance 3 and the bound is not measurable on this link. The download
runs on the tcp lane, which is one SSH connection with the same yamux
window as before, so the design has no mechanism for a lower rate. The
connect time with four lanes is at most one second above the single lane,
within the 3 s bound of acceptance 4.

## Decisions

1. **The lanes ship as the SSH transport.** On a direct-path remote without
   the UDP grant they keep a DNS query and an echo at the idle round-trip
   time during a bulk download, against 300 to 490 ms without them.
2. **The UDP grant stays the fix for a relayed remote.** The lanes take the
   query under load from 466 to 590 ms to the relay queue of 133 to 147 ms,
   and QUIC with BBR kept it at 74 ms in spike 9, because BBR keeps that
   queue short.
3. **Acceptance 2 on the relayed path is recorded as not met by the 3 times
   idle criterion**, and met by the criterion of acceptance 1 for that path,
   the connect next to the session. The story's pre-measurement predicted
   this, and the tables show the mechanism: every probe through the session
   tracks the connect next to it within a few milliseconds.
4. **The bulk rate bound of acceptance 3 is not measurable on the direct
   path with this link**, and it holds on the relayed path.
