# Spike 8: the routing loop between a session and tailscaled

## Goal

Find why DNS through a session to a relayed remote answered only part of the
queries, in bursts, on the SSH transport and on the QUIC transport alike, and
why the session ended after a few minutes with "session ended: mux closed".

## Symptom

Measured on 2026-09-12 on a Linux client with tj 1.3.0, on a session to a
private-subnet remote behind a NAT gateway, relayed through DERP, with the DNS
mode `all` and the remote's VPC resolver. `dig` with a 2 s limit, one try per
query, against the resolver through the session, and one `tailscale ping` with
a 1.5 s limit to the remote between the queries.

| Session | DNS answered within 2 s | Tailnet pings answered | Path switches in the tailscaled log |
| -- | -- | -- | -- |
| The VPC routed, as computed | 14 of 33 | 23 of 33 | every 15 to 20 s |
| The same, with `--exclude <remote VPC address>/32` | 24 of 24 | 24 of 24 | 0 in 2 min |

The resolver answered the remote itself 30 of 30, the relayed path answered
40 of 40 pings at 22 ms with no session up, and the remote had no memory or
CPU pressure.

## Root cause

The session networks contain the remote's own VPC address, and the remote's
tailscaled advertises that address as a WireGuard endpoint. tailscaled on the
client marks its UDP packets with the bypass mark `0x80000`, and its policy
rules 5210 to 5250 send a marked packet to the main table, the default table,
and then to unreachable. The session routes were in the main table, so the
discovery pings to the remote's VPC address followed the `tj0` route into the
tunnel, reached the remote's tailscaled through the helper, and came back the
same way. magicsock then logged `now using <remote VPC address>:41641` and
moved the WireGuard traffic to that path. That path runs inside the tunnel
that it carries, so it blackholed, magicsock fell back to DERP, the pings
succeeded again, and the cycle repeated. The remote's log shows the mirror
image: `now using <remote VPC address>:<helper port>` and `new contact
via=derp` in alternation.

The same cycle stalls the SSH connection that carries the yamux control
stream. One stall over the 30 s yamux keepalive limit ends the session with
"mux closed", which happened 2.5 min after the session came up.

The client's tailscaled log holds the same switch for the morning session of
K8S-206 section 2, to the old gateway's VPC address, during the measurement
that produced the "congestion window 1" numbers. The loop explains that
collapse without any relay loss. The log also holds the switch for a session
to a remote with a direct IPv6 path: the remote's global VPC IPv6 address is
its real direct endpoint and is inside the routed /56, so a session captured
the direct path and ran over DERP between the flaps.

## Fix

The Linux router puts the session routes in the routing table 117 behind the
rule `pref 5300 lookup 117`, for IPv4 and for IPv6. Marked packets consult
main and default only, find no session route, and leave through the real
interface, where a private VPC address is unreachable and the fake path never
appears. Unmarked packets miss tailscaled's table 52 at rule 5270 and reach
table 117 at rule 5300, so every destination in the session networks,
including the remote's own address, stays reachable through the tunnel.
`Router.Reset` flushes the table and deletes the rules on every exit path.

tailscaled protects its packets in one of two ways on Linux: the bypass mark
when `SO_MARK` works, and a socket bound to the default interface when it does
not. A bound socket never sends through `tj0`, so the marked case is the only
one where a session route can capture the transport, and the table covers it.
On macOS tailscaled binds by interface, so the routes stay in the main table.

The session also polls the local API every 30 s and logs one warning when
tailscaled's current endpoint for the remote is inside the session networks.

## What the rig cannot see

The rig container mounts the host's tailscaled socket and has its own network
namespace. No discovery ping of the host's tailscaled ever meets the
container's routes, so the rig never loops. Every rig number of spikes 5 to 7
is valid for the data plane and blind to this bug. The rig checks that the
routes are in table 117 behind the rules and that both are gone after
`disconnect` and after a crash. The proof of the fix is a session on a client:
zero `now using` lines in the tailscaled journal for the remote's own
addresses, and 100% DNS and ping answers over three minutes.
