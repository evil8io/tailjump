# Spike 8: the routing loop between a session and tailscaled

## Goal

Find why only part of the DNS queries through a session to a relayed remote got an answer. The answers came in bursts, on the SSH transport and on the QUIC transport alike. Also find why the session ended after a few minutes with "session ended: mux closed".

## Symptom

The measurement is from 2026-09-12, on a Linux client with tj 1.3.0. The session went to a private-subnet remote behind a NAT gateway, relayed through DERP. The DNS mode was `all`, with the remote's VPC resolver. `dig` sent the queries to the resolver through the session, with a 2 s limit and one try per query. Between the queries, `tailscale ping` sent one ping with a 1.5 s limit to the remote.

| Session | DNS answered within 2 s | Tailnet pings answered | Path switches in the tailscaled log |
| -- | -- | -- | -- |
| The VPC routed, as computed | 14 of 33 | 23 of 33 | every 15 to 20 s |
| The same, with `--exclude <remote VPC address>/32` | 24 of 24 | 24 of 24 | 0 in 2 min |

On the remote itself, the resolver answered 30 of 30 queries. Without a session, 40 of 40 pings over the relayed path got an answer at 22 ms. The remote did not have memory pressure or CPU pressure.

## Root cause

The session networks contain the remote's own VPC address. The remote's tailscaled advertises that address as a WireGuard endpoint. tailscaled on the client marks its UDP packets with the bypass mark `0x80000`. Under its policy rules 5210 to 5250, a marked packet goes to the main table, then to the default table, and then to unreachable.

The session routes were in the main table. Thus the discovery pings to the remote's VPC address followed the `tj0` route into the tunnel. The pings reached the remote's tailscaled through the helper and came back the same way. magicsock then logged `now using <remote VPC address>:41641` and
moved the WireGuard traffic to that path.

That path is inside the tunnel, but the tunnel itself depends on that path. Thus all traffic on that path was dropped. Then magicsock fell back to DERP, and the pings succeeded again. Then the cycle repeated. The log of the remote contains the same cycle from the other side: `now using <remote VPC address>:<helper port>` and `new contact via=derp` in alternation.

During the same cycle, the SSH connection with the yamux control stream stalls. When one stall is longer than the 30 s yamux keepalive limit, the session ends with "mux closed". In this case, that happened 2.5 min after the session came up.

The client's tailscaled log contains the same switch for the morning session of section 2 of the v2 transport spec. In that session, the switch went to the old gateway's VPC address, during the measurement of the "congestion window 1" numbers. The loop alone is a sufficient cause of that collapse, without any relay loss.

The log also contains the switch for a session to a remote with a direct IPv6 path. The remote's global VPC IPv6 address is its real direct endpoint and is inside the routed /56. Thus the session captured the direct path and ran over DERP between the flaps.

## Fix

The Linux router puts the session routes in the routing table 117 behind the
rule `pref 5300 lookup 117`, for IPv4 and for IPv6. A marked packet is looked up in the main table and the default table only. Those tables do not contain a session route, so the packet leaves through the real interface. On that interface, a private VPC address is unreachable. Thus the fake path does not appear.

Unmarked packets do not match tailscaled's table 52 at rule 5270, and they reach table 117 at rule 5300. Thus every destination in the session networks stays reachable through the tunnel, including the remote's own address.
`Router.Reset` flushes the table and deletes the rules on every exit path.

On Linux, tailscaled protects its packets in one of two ways. When `SO_MARK` works, tailscaled uses the bypass mark. When `SO_MARK` does not work, tailscaled uses a socket bound to the default interface.

A bound socket does not send through `tj0`. Thus a session route can capture the transport only in the marked case. Table 117 is the fix for that case.
On macOS tailscaled binds by interface, so the routes stay in the main table.

The session also samples tailscaled's endpoint for the remote every 10 s. When the path changes three or more times within two minutes, the session logs one warning. The first version of the watch warned on an endpoint inside the session networks. That warning also appeared for the real direct IPv6 endpoint of the shared gateway. That endpoint is inside the routed /56, but it is not a loop.

## What the rig cannot see

The rig container mounts the host's tailscaled socket and has its own network
namespace. The discovery pings of the host's tailscaled do not go through the container's routes. Thus the loop does not occur in the rig. Every rig number of spikes 5 to 7 is valid for the data plane, but this bug is not visible in those numbers.

The rig checks that the routes are in table 117 behind the rules. It also checks that the routes and the rules are removed after `disconnect` and after a crash. The proof of the fix is a session on a client. In that session, the tailscaled journal contains zero `now using` lines for the remote's own addresses. 100% of the DNS queries and the pings get an answer over three minutes.
