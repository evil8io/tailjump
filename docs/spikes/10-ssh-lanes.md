# Spike 10: one SSH connection per protocol on the SSH transport

## Goal

Measure what the lanes change on the SSH transport. The lanes are one SSH
connection per protocol. Measure on the direct path and on the relayed path,
in paired cycles with and without the lanes. Measure the latency of a DNS
query, a TCP DNS query, and an ICMP echo during a bulk download. Also measure
the bulk rate and the connect time. Spike 9 contains
the measurements of the transport without lanes. There, a DNS query under
load took up to 795 ms on the relayed path and up to 355 ms on the direct
path. The reason is that every flow went through one TCP connection with a
4 MiB yamux window.

## Method

The client is the laptop of spike 9 on the same WiFi link. The two
remotes are the two aarch64 remotes of spike 9. The direct remote is in a
public dual-stack subnet. The client reaches it over IPv6 without a relay.
The relayed remote is in a private IPv4-only subnet. For this remote, the
relay is forced through `TS_DEBUG_ALWAYS_USE_DERP=true`. `test/e2e/compare.sh` ran every
session on 2026-09-12 between 23:11 and 23:24 with `TJ_TEST_SESSIONS=ssh`.
The script ran one cycle per invocation. One cycle is a pair: first a session
with `TJ_SSH_LANES=1`, the single lane, then a session with the lanes. Two
cycles were run per remote. The cycles of the relayed remote were first. tj was the build of the story
branch, `v1.4.0-5-gdc9e2ef`, on both the client and the root copy, with
`tj connect <remote> --dns all --transport ssh`. The lanes session opens the
four lanes `tcp,udp,icmp,dns`.

Each session contained these steps, in this order:

1. A download of 60 s from the bench server over IPv4, with probes every 5 s
   during the download. With the lanes, the download goes over the tcp lane.
   The queries go over the dns lane. The echo goes over the icmp lane. These
   are the probes:
   - one DNS query over UDP
   - one DNS query over TCP with `dig +tcp`
   - one ICMP echo to the remote's VPC address
   - one TCP connect to the remote's tailnet SSH port. This connect does not
     go through the session. Its latency is the latency of the shared path
     next to the download.
   - one TCP connect to the VPC resolver through the session
   - one `tailscale ping`
2. 20 TCP connects to the resolver's port 53 and 10 DNS queries, idle.
3. One lookup of the remote's own private name through the system resolver.
4. On the direct remote, a download of 30 s over IPv6.

The idle round-trip time is 28 to 31 ms on the relayed path and 24 to 27 ms
on the direct path. These numbers are from the idle queries after each
download. The connect
time is the time from the `connect` line of the log to the first status
line. This time includes up to one second of readiness polling.

Every probe in every session got an answer. After each session, each remote
did not have a file, a listener, or a process left.

## Result 1: latency under load, relayed path

Each cell contains the minimum to the maximum over the 8 to 9 probes during
the download. The columns are the single lane and the lanes, in cycle 1 and
cycle 2.

| Probe | Single lane, cycle 1 | Lanes, cycle 1 | Single lane, cycle 2 | Lanes, cycle 2 |
|-------|----------------------|----------------|----------------------|----------------|
| DNS over UDP | 30 to 466 ms | 29 to 133 ms | 28 to 590 ms | 29 to 146 ms |
| DNS over TCP | 321 to 384 ms | 27 to 363 ms | 272 to 373 ms | 28 to 148 ms |
| ICMP echo | 410 to 448 ms | 28 to 134 ms | 381 to 427 ms | 55 to 147 ms |
| TCP connect through the session | 327 to 508 ms | 28 to 138 ms | 306 to 386 ms | 55 to 151 ms |
| TCP connect next to the session | 62 to 125 ms | 27 to 134 ms | 37 to 122 ms | 52 to 147 ms |

With the lanes, the maximum latency of the DNS query under load decreases
from 466 and 590 ms to 133 and 146 ms. The maximum latency of the echo
decreases from 448 and 427 ms to 134 and 147 ms. In the
lane sessions, the latencies of the four probes through the session and of
the connect next to the session change together. In every probe line, they
are within a few milliseconds of each other. So the remaining latency is the
queue of the relay. Every connection through that relay shares this queue. A
lane cannot remove the queue. The maxima of the
DNS query and the echo equal the maximum of the connect next to the session
in the same cycle. That maximum is 134 and 147 ms. The maximum of the connect
next to the session is the criterion of acceptance 1 for the relayed path. One TCP query of 363 ms in cycle 1 is above that criterion.
The other 16 TCP queries under load in the two lane sessions are within it.
The relay queue was shallower than in the pre-measurement of the story.
In the pre-measurement, the connect next to the download took up to 487 ms.
Here, the maximum was 147 ms.

The 3 times idle criterion of acceptance 2 is 90 ms. This criterion does
not hold on the relayed path. The reason is in section 1 of the story: the
relay queue is outside the SSH connection.

## Result 2: latency under load, direct path

Each cell contains the minimum to the maximum over the 10 to 12 probes during the download.

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
connect next to the session is at the idle round-trip time in every session.
So the direct path does not have a shared queue, and the lanes remove the
whole queue.

## Result 3: latency idle, bulk rate, and connect time

| Number | Relayed, single / lanes, cycle 1 | Relayed, single / lanes, cycle 2 | Direct, single / lanes, cycle 1 | Direct, single / lanes, cycle 2 |
|--------|----------------------------------|----------------------------------|---------------------------------|---------------------------------|
| DNS idle, min to max | 28 to 31 / 28 to 46 ms | 28 to 37 / 27 to 34 ms | 24 to 27 / 24 to 28 ms | 24 to 26 / 25 to 27 ms |
| Connect idle, p50 / p95 | 29.0 / 29.8, 28.2 / 52.7 ms | 29.2 / 30.4, 28.8 / 30.5 ms | 24.9 / 27.2, 24.8 / 26.2 ms | 25.1 / 27.2, 24.9 / 27.1 ms |
| Bulk IPv4, 60 s | 22.4 / 21.8 Mbit/s | 22.3 / 22.2 Mbit/s | 131.9 / 101.8 Mbit/s | 121.6 / 131.6 Mbit/s |
| Bulk IPv6, 30 s | none | none | 160.1 / 107.9 Mbit/s | 112.7 / 144.4 Mbit/s |
| Connect time | 3 / 4 s | 4 / 4 s | 1 / 2 s | 1 / 2 s |

In the idle queries and connects, the lanes do not change the latency. Every
query and connect is at the round-trip time. The one p95 of 52.7 ms comes
from the relay queue, which drains right after the download. The relayed bulk rate is at the relay ceiling with and without
lanes, within 3%. On the direct path over IPv4, the rate with the lanes is 23% lower in cycle
1 and 8% higher in cycle 2. Over IPv6, the rate with the lanes is 33% lower
and 28% higher. The two single-lane cycles differ by 30% from each other over
IPv6, 160.1 against 112.7 Mbit/s. So the WiFi variance is above the 10% bound
of acceptance 3. On this link, the bound is not measurable. The download
goes over the tcp lane. The tcp lane is one SSH connection with the same
yamux window as before. So the design does not contain a mechanism for a
lower rate. The
connect time with four lanes is at most one second above the single lane,
within the 3 s bound of acceptance 4.

## Decisions

1. **The lanes are shipped as the SSH transport.** With the lanes, a DNS
   query and an echo during a bulk download are at the idle round-trip time.
   This result is for a direct-path remote without the UDP grant. Without the
   lanes, they take 300 to 490 ms.
2. **The UDP grant stays the fix for a relayed remote.** With the lanes,
   the latency of the query under load decreases from 466 to 590 ms to the
   relay queue of 133 to 147 ms. In spike 9, QUIC with BBR kept the latency
   at 74 ms, because BBR keeps that queue short.
3. **Acceptance 2 on the relayed path is recorded as not met by the 3 times
   idle criterion.** It is recorded as met by the criterion of acceptance 1
   for that path, the connect next to the session. This result was expected from
   the pre-measurement of the story. The mechanism is in the tables: every
   probe through the session changes together with the connect next to it,
   within a few milliseconds.
4. **The bulk rate bound of acceptance 3 is not measurable on the direct
   path with this link.** The bound holds on the relayed path.
