# Spike 7: QUIC datagrams against streams for UDP flows

## Goal

Decide whether a UDP flow of a session should run on a QUIC datagram, RFC 9221. Today, the flow runs on a QUIC stream. Chunk 4 of the v2 transport spec is optional. Its rule is a measurement. Build the datagram path when all of these conditions are true:

* On a clean path, the datagram p50 is at least 10% below the stream p50.
* On a 30 ms path, the datagram p50 is at least 10% below the stream p50.
* Under loss, the count of replies above 2 s is not above the count of the stream mode.

Hysteria2 carries UDP on datagrams with a session id and fragments a payload
that does not fit one packet, so the design is known. The open question is the
gain.

## Method

The spike tool is `test/e2e/datagram`. It is measurement code and is not part of tj. It runs a QUIC server and a client in one process over 127.0.0.1. Both sides use the `quic.Config` of `internal/mux/quic.go`, with `EnableDatagrams` set to true. As the transport does, the tool installs BBR right after the handshake.

The tool ran in a container from the rig image with `CAP_NET_ADMIN`. The container did not have a TUN device or a session, because the two endpoints do not need a remote.

In each mode, the tool sent 200 request-reply round trips of DNS size, one at a time. The client timeout was 2 s with one retry, as with `dig +time=2 +tries=2`. The latency of a request is the time from its first send to its reply. Thus the latency of a request that needed the retry includes the timeout, as for a DNS client. A request is unanswered when a reply did not arrive for either attempt. Unanswered requests are not in the percentiles.

* Stream mode: the client opens one new bidirectional stream per request. The client writes the stream kind, the destination, and one 2-byte length frame with 60 bytes of payload. Then the client reads one frame with 120 bytes and ends the stream. That is 70 bytes out and 122 bytes back. A tj UDP flow does the same per 4-tuple.
* Datagram mode: the client calls `SendDatagram` once per request, with an 8-byte session id and a 4-byte packet id before the 60 bytes. The reply is one datagram. That is
  72 bytes out and 132 bytes back.

The tool ran each mode under three conditions:

* No shaping.
* `tc netem delay 30ms` on `lo`.
* `tc netem delay 30ms loss 7%` on `lo`.

Every number is the median of 3 runs of 200. Loopback egress is traversed once per direction. Thus the loss is 7% in each direction. The round trip is 60 ms longer. The shaping is the same as in spike 5. The
container's outward interface is a user-mode NAT whose qdisc does not shape the
path, so `lo` is the shaping point.

The second part is the baseline through a real session. `test/e2e/loss.sh` now records the p50 and the p95 of dig's "Query time" beside the share answered within 2 s. A latency change is not visible in the share alone. It ran from the
rig to the shared gateway on the QUIC transport, clean and under loss, with 20
queries to the VPC resolver each.

Every measurement is from 2026-09-12, on a WiFi client with kernel 7.0.
Fragmentation is not measured, because the latency numbers were sufficient for the decision on the rule.
The maximum datagram payload at `InitialPacketSize` 1232 is about 1200 bytes, so
a DNS answer with EDNS above that would need it.

## Result 1: no shaping

| Measure | Stream mode | Datagram mode |
| -- | -- | -- |
| p50 | 0.078 ms (0.078, 0.072, 0.078) | 0.047 ms (0.049, 0.047, 0.046) |
| p95 | 0.122 ms (0.122, 0.093, 0.183) | 0.061 ms (0.061, 0.117, 0.060) |
| Answered of 200 | 200 | 200 |
| Replies above 2 s | 0 | 0 |

The datagram mode is 0.031 ms faster per request, which is 40% of the 0.078 ms of the stream mode. Under this condition, the only difference is the CPU work of the two flow protocols, because a stream open does not need a round trip. The client writes the first frame at once.

## Result 2: 30 ms delay in each direction

| Measure | Stream mode | Datagram mode |
| -- | -- | -- |
| p50 | 60.626 ms (60.595, 60.626, 60.786) | 60.849 ms (60.849, 60.901, 60.846) |
| p95 | 60.965 ms (60.965, 60.952, 61.021) | 61.282 ms (61.259, 61.282, 61.283) |
| Answered of 200 | 200 | 200 |
| Replies above 2 s | 0 | 0 |

The datagram mode is 0.223 ms slower, which is 0.4%. Each of its three runs is slower than each of the three stream runs. The cause is not measured. The 0.031 ms of Result 1 is 0.05% of this round trip. Thus the saving of the flow protocol is not visible on a path with a delay.

## Result 3: 7% loss and 30 ms delay in each direction

| Measure | Stream mode | Datagram mode |
| -- | -- | -- |
| p50 | 60.781 ms (60.912, 60.695, 60.781) | 60.723 ms (60.606, 60.723, 60.887) |
| p95 | 149.310 ms (149.310, 149.139, 149.390) | 2062.014 ms (2061.594, 2062.014, 2062.372) |
| Answered of 200 | 200 (200, 200, 200) | 193 (196, 193, 194) |
| Replies above 2 s | 0 | 21 (21, 20, 22) |
| Answered within 2 s of 200 | 200 | 173 (175, 173, 172) |

The p50 is equal, because half the requests do not lose a packet. The tail is not equal.

QUIC's loss recovery repairs a lost packet of a stream. Thus the stream p95 of 149 ms is one probe timeout above the round trip. All 600 requests were answered. A lost datagram is not repaired, so the client waits its full 2 s and retries. 21 replies of 200 arrive above 2 s. 4 to 7 requests of 200 lose both attempts and do not get an answer.

27 requests of 200 lost at least one packet. That is 13.5%, which is the expected share for 7% loss in each direction. This is the check that the shaping worked as configured.

The last row is the acceptance criterion in section 10 of the v2 transport spec. Under this loss, at least 95% of the DNS queries must be answered within 2 s. In the stream mode, 100% of the queries are answered within 2 s. In the datagram mode, 86.5% are answered, so the datagram path would fail that criterion. The two modes ran over the loopback and not through a session. Thus the row is the contribution of the flow protocol to the criterion, and not a session measurement.

## Result 4: the tj-level stream baseline

The extended `test/e2e/loss.sh` measured 20 queries to the VPC resolver with `dig +time=2 +tries=1`. The queries went through a tj session from the rig to the shared gateway on the QUIC transport. This is the stream path as it ships.

| Path | Query time p50 | Query time p95 | Answered within 2 s |
| -- | -- | -- | -- |
| clean | 31 ms | 35 ms | 20 of 20 |
| 7% loss and 30 ms delay in each direction | 92 ms | 318 ms | 20 of 20 |

The clean p50 of 31 ms is the path to the gateway plus the resolver. It is consistent with the TCP connect p50 of 30.7 ms in spike 6. It is also consistent with the 31 ms DNS query of the chunk 2 status measurement. The loss row is similar to the stream column of Result 3. The p50 stays at one round trip. The p95 is about one probe timeout higher.

## Decisions

1. **The datagram path is not built, and chunk 4 closes as not needed.** The rule needs the datagram p50 at least 10% below the stream p50, on the clean path and on the 30 ms path. Under loss, the count of replies above 2 s must not be higher than in the stream mode. The clean path passes at 40% below, but the 30 ms path fails, because the datagram mode is 0.4% slower. The loss condition fails, because 21 replies of 200 arrive above 2 s, against 0. Also, 4 to 7 requests of 200 do not get an answer, against 0. Two of the three conditions fail, so code is not shipped.
2. **The clean-path saving is 0.031 ms per flow.** Against the session's DNS p50
   of 31 ms in Result 4 that is 0.1%. The saving is CPU work per flow. Thus the saving would matter only at a rate of thousands of short UDP flows per second. With the measured 0.031 ms, 1000 flows per second need 3% of one core. A session of one engineer does not reach such a rate.
3. **The reason to keep the stream path is structural, not a tuning result.**
   QUIC's loss recovery repairs a lost packet of a stream within about one probe timeout. A lost datagram is not repaired. Thus the only repair is the retry of the DNS client after its own 2 s timeout. A tunnel for DNS needs the repair that is already in the stream path. A change in the congestion controller, the windows, or the packet size does not change that.
4. **The query time p50 and the p95 ship in `test/e2e/loss.sh`.** They are in
   the per-transport table and in the RESULT line. Result 4 is the recorded baseline of the stream path for a later comparison.
5. **The datagram design of the story is not implemented and stays unmeasured.**
   That design contains these parts:

   * The session id.
   * The destination on the first datagram.
   * The fragment header and the reassembly with a short timeout.
   * The fallback to the stream path when the peer does not advertise datagram support.

   `EnableDatagrams` stays false on both sides, so the transport parameter is absent. Thus each peer does not detect datagram support on the other peer.

### What the v2 transport spec assumed

* In the UDP flow row of section 5 and in the Hysteria2 item of section 11, the datagram path is a latency gain for DNS. The story does not contain a measurement for that row. This spike is the measurement: the gain exists only on a path without delay and without loss.
* The cost of the retry is not in the same items. Under loss, the dominant cost of a UDP flow is the 2 s timeout of the DNS client, not the flow protocol. The client reaches that timeout on every path except the stream path.
