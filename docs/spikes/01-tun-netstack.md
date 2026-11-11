# Spike 1: TUN plus netstack on Linux

## Goal

Confirm that a userspace network stack over a TUN device works without firewall rules. Measure throughput against sshuttle on the same remote.

## Result

Viable and fast. A TUN device with a gVisor netstack carries TCP and UDP over one SSH channel and needs no firewall rule; the capture is a route.

Each value is the median of 3 downloads of 256 MiB from the shared gateway over a WiFi client. Across runs, the throughput of the raw SSH channel alone is 49 to 64 Mbit/s, so settings were compared as paired cycles, not as single medians.

| Path | Mbit/s |
| -- | -- |
| Tailnet direct, no SSH | 76.6 |
| Raw SSH channel | 53.7 |
| tj IPv6, 4 MiB window | 65.9 |
| tj IPv4, 4 MiB window | 51.8 |
| tj IPv4, 256 KiB window | 33.0 |
| sshuttle 1.3.2 | 8.5 |

tj reaches 96% of the raw SSH channel and 6.1 times sshuttle. During one 256 MiB download, the client uses 7.1% of one core and the helper uses 1.7%.

## Findings

- The only setting with a measurable effect is the yamux window at 4 MiB. The 256 KiB default equals the path's bandwidth-delay product.
- The netstack receive buffer and SACK do not have an effect, because the only connection the netstack terminates is the lossless local leg over the TUN.
- Read and write the TUN device in batches of 128. With a one-buffer loop, the rest of a GRO super-packet is dropped.
- The NIC needs promiscuous mode and spoofing and no assigned address.
- Give the device a global-scope ULA as well as a link-local address. Without the ULA, a client without global IPv6 does not have an IPv6 source.

These findings are recorded in `docs/architecture.md`.
