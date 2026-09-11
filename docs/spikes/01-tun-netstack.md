# Spike 1: TUN plus netstack on Linux

## Goal

Confirm that a userspace network stack over a TUN device works without firewall rules, and measure throughput against sshuttle on the same remote.

## Result

Viable and fast. A TUN device with a gVisor netstack carries TCP and UDP over one SSH channel and needs no firewall rule; the capture is a route.

Download of 256 MiB, median of 3 runs, from the shared gateway over a WiFi client. The raw SSH row alone spans 49 to 64 Mbit/s across runs, so settings were compared as paired cycles, not single medians.

| Path | Mbit/s |
| -- | -- |
| Tailnet direct, no SSH | 76.6 |
| Raw SSH channel | 53.7 |
| tj IPv6, 4 MiB window | 65.9 |
| tj IPv4, 4 MiB window | 51.8 |
| tj IPv4, 256 KiB window | 33.0 |
| sshuttle 1.3.2 | 8.5 |

tj reaches 96% of the raw SSH channel and 6.1 times sshuttle. One 256 MiB download costs the client 7.1% of one core and the helper 1.7%.

## Findings

- The only setting with a measurable effect is the yamux window at 4 MiB. The 256 KiB default equals the path's bandwidth-delay product.
- The netstack receive buffer and SACK have no effect, because the only connection the netstack terminates is the lossless local leg over the TUN.
- Read and write the TUN device in batches of 128; a one-buffer loop drops the rest of a GRO super-packet.
- The NIC needs promiscuous mode and spoofing and no assigned address.
- Give the device a global-scope ULA as well as a link-local address, or a client without global IPv6 has no IPv6 source.

These findings are recorded in `docs/architecture.md`.
