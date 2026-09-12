# Spike 7: QUIC datagrams against streams for UDP flows

## Goal

Decide whether a UDP flow of a session should run on a QUIC datagram, RFC 9221,
instead of the QUIC stream it uses today. Chunk 4 of K8S-206 is optional, and
its rule is a measurement: build the datagram path when the datagram p50 is at
least 10% below the stream p50 on a clean path and on a 30 ms path, and when the
count of replies above 2 s under loss is not above the stream mode's count.
Hysteria2 carries UDP on datagrams with a session id and fragments a payload
that does not fit one packet, so the design is known. The open question is the
gain.

## Method

The spike tool is `test/e2e/datagram`. It is measurement code and no part of
tj. It runs a QUIC server and a client in one process over 127.0.0.1, with the
`quic.Config` of `internal/mux/quic.go` plus `EnableDatagrams` true on both
sides, and it installs BBR right after the handshake as the transport does. It
ran in a container from the rig image with `CAP_NET_ADMIN`, with no TUN device
and no session, because the two endpoints need no remote.

Each mode sent 200 request-reply round trips of DNS size, one at a time, with a
client timeout of 2 s and one retry, which is what `dig +time=2 +tries=2` does.
The latency of a request is the time from its first send to its reply, so a
request that needed the retry carries the timeout, as a DNS client sees it. A
request that neither attempt answered counts as unanswered and is left out of
the percentiles.

* Stream mode: one new bidirectional stream per request. The client writes the
  stream kind, the destination, and one 2-byte length frame with 60 bytes of
  payload, reads one frame with 120 bytes, and ends the stream. That is 70 bytes
  out and 122 bytes back, and it is what a tj UDP flow does per 4-tuple.
* Datagram mode: one `SendDatagram` per request, with an 8-byte session id and a
  4-byte packet id before the 60 bytes, and the reply as one datagram. That is
  72 bytes out and 132 bytes back.

Each mode ran under three conditions, and every number is the median of 3 runs
of 200: no shaping, `tc netem delay 30ms` on `lo`, and `tc netem delay 30ms
loss 7%` on `lo`. Loopback egress is traversed once per direction, so both
directions lose 7% and the round trip gains 60 ms, as spike 5 did. The
container's outward interface is a user-mode NAT whose qdisc does not shape the
path, so `lo` is the shaping point.

The second part is the baseline through a real session. `test/e2e/loss.sh` now
records the p50 and the p95 of dig's "Query time" beside the share answered
within 2 s, because the share alone cannot see a latency change. It ran from the
rig to the shared gateway on the QUIC transport, clean and under loss, with 20
queries to the VPC resolver each.

Every measurement is from 2026-09-12, on a WiFi client with kernel 7.0.
Fragmentation is not measured, because the rule closed on the latency numbers.
The maximum datagram payload at `InitialPacketSize` 1232 is about 1200 bytes, so
a DNS answer with EDNS above that would need it.

## Result 1: no shaping

| Measure | Stream mode | Datagram mode |
| -- | -- | -- |
| p50 | 0.078 ms (0.078, 0.072, 0.078) | 0.047 ms (0.049, 0.047, 0.046) |
| p95 | 0.122 ms (0.122, 0.093, 0.183) | 0.061 ms (0.061, 0.117, 0.060) |
| Answered of 200 | 200 | 200 |
| Replies above 2 s | 0 | 0 |

The datagram mode saves 0.031 ms per request, which is 40% of the stream mode's
0.078 ms. This condition measures the CPU work of the two flow protocols and
nothing else, because a stream open costs no round trip: the client writes the
first frame at once.

## Result 2: 30 ms delay in each direction

| Measure | Stream mode | Datagram mode |
| -- | -- | -- |
| p50 | 60.626 ms (60.595, 60.626, 60.786) | 60.849 ms (60.849, 60.901, 60.846) |
| p95 | 60.965 ms (60.965, 60.952, 61.021) | 61.282 ms (61.259, 61.282, 61.283) |
| Answered of 200 | 200 | 200 |
| Replies above 2 s | 0 | 0 |

The datagram mode is 0.223 ms slower, which is 0.4%, and each of its three runs
is slower than each of the three stream runs. The cause is not measured. The
0.031 ms of Result 1 is 0.05% of this round trip, so the saving of the flow
protocol disappears as soon as the path has a delay.

## Result 3: 7% loss and 30 ms delay in each direction

| Measure | Stream mode | Datagram mode |
| -- | -- | -- |
| p50 | 60.781 ms (60.912, 60.695, 60.781) | 60.723 ms (60.606, 60.723, 60.887) |
| p95 | 149.310 ms (149.310, 149.139, 149.390) | 2062.014 ms (2061.594, 2062.014, 2062.372) |
| Answered of 200 | 200 (200, 200, 200) | 193 (196, 193, 194) |
| Replies above 2 s | 0 | 21 (21, 20, 22) |
| Answered within 2 s of 200 | 200 | 173 (175, 173, 172) |

The p50 is equal, because half the requests lose no packet. The tail is not. A
lost packet of a stream is repaired by QUIC's loss recovery, so the stream p95
of 149 ms is one probe timeout above the round trip, and all 600 requests were
answered. A lost datagram is not repaired, so the client waits its full 2 s and
retries: 21 replies of 200 arrive above 2 s, and 4 to 7 requests of 200 lose
both attempts and get no answer at all.

The 27 requests of 200 that lost at least one packet match the 13.5% that 7%
loss in each direction predicts, which is the check that the shaping did what it
says.

The last row is the acceptance criterion of K8S-206 section 10, which asks that
at least 95% of the DNS queries are answered within 2 s under this loss. The
stream mode answers 100% and the datagram mode answers 86.5%, so the datagram
path would fail that criterion. The two modes ran over the loopback and not
through a session, so the row is the flow protocol's contribution to the
criterion and not a session measurement.

## Result 4: the tj-level stream baseline

Through a tj session from the rig to the shared gateway on the QUIC transport,
20 queries to the VPC resolver with `dig +time=2 +tries=1`, measured by the
extended `test/e2e/loss.sh`. This is the stream path as it ships.

| Path | Query time p50 | Query time p95 | Answered within 2 s |
| -- | -- | -- | -- |
| clean | 31 ms | 35 ms | 20 of 20 |
| 7% loss and 30 ms delay in each direction | 92 ms | 318 ms | 20 of 20 |

The clean p50 of 31 ms is the path to the gateway plus the resolver, and it
agrees with the TCP connect p50 of 30.7 ms in spike 6 and with the 31 ms DNS
query of the chunk 2 status measurement. The loss row has the shape of the
stream column of Result 3: the p50 holds at one round trip, and the p95 grows by
about one probe timeout.

## Decisions

1. **The datagram path is not built, and chunk 4 closes as not needed.** The
   rule needs the datagram p50 at least 10% below the stream p50 on the clean
   path and on the 30 ms path, and no more replies above 2 s under loss. The
   clean path passes at 40% below. The 30 ms path fails, because the datagram
   mode is 0.4% slower. The loss condition fails, because 21 replies of 200
   arrive above 2 s against 0, and 4 to 7 requests of 200 get no answer against
   0. Two of the three legs fail, so no code ships.
2. **The clean-path saving is 0.031 ms per flow.** Against the session's DNS p50
   of 31 ms in Result 4 that is 0.1%. The saving is CPU work per flow, so it
   would only matter at a rate of thousands of short UDP flows per second: the
   arithmetic on the measured 0.031 ms puts 1000 flows per second at 3% of one
   core. One engineer's session has no such rate.
3. **The reason to keep the stream path is structural, not a tuning result.**
   QUIC's loss recovery repairs a lost packet of a stream within about one probe
   timeout, and nothing repairs a lost datagram, so the DNS client's own 2 s
   timeout becomes the repair. A tunnel that carries DNS wants the repair that
   the stream already has. A change in the congestion controller, the windows,
   or the packet size does not move that.
4. **The query time p50 and the p95 ship in `test/e2e/loss.sh`.** They are in
   the per-transport table and in the RESULT line, and Result 4 is the stream
   path's recorded baseline for a later comparison.
5. **The datagram design of the story is not implemented and stays unmeasured.**
   That covers the session id, the destination on the first datagram, the
   fragment header and the reassembly with a short timeout, and the fallback to
   the stream path when the peer advertises no datagram support.
   `EnableDatagrams` stays false on both sides, so the transport parameter is
   absent and each peer sees no datagram support from the other.

### What K8S-206 assumed

* The UDP flow row of section 5 and the Hysteria2 item of section 11 treat the
  datagram path as a latency gain for DNS. The story gives no measurement for
  that row, and this spike is the measurement: the gain exists only on a path
  with no delay and no loss.
* The same items do not weigh the retry. Under loss the dominant cost of a UDP
  flow is not the flow protocol but the DNS client's 2 s timeout, and only the
  stream path keeps the client from reaching it.
