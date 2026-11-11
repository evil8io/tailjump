# Spike 9: tj against sshuttle on the direct and the relayed path

## Goal

Measure tj and sshuttle from one client on the same day, in paired cycles. Do the measurements on a remote with a direct Tailscale path and on a remote that Tailscale relays through DERP. Measure these values:

* Bandwidth.
* Latency under load and idle.
* Reliability.

The story is the comparison of tj and sshuttle. This document contains the consequences of the numbers for the migration to the gateway module of the infrastructure repository. It also contains the parts of the migration that stay the same.

## Method

The client is a Linux laptop on WiFi 6, 80 MHz, two spatial streams, with a
receive rate of 960 Mbit/s at the physical layer. The real direct path is not available in the rig. Thus the campaign ran on the client itself, with the user's permission, one tunnel at a time. `test/e2e/compare.sh` ran every session on 2026-09-12 between 20:57 and 21:31. The tables below are a summary of the lines that the script printed. `task e2e:compare` runs the script.

Versions and flags:

* tj 1.4.0, `tj connect <remote> --dns all --transport quic` for the QUIC rows
  and `--transport ssh` for the SSH-transport rows. The root copy under
  `/usr/local/libexec/tj` must match the client binary, or `connect` refuses.
* sshuttle 1.3.2 with the flags of the old `connect` task of the infrastructure repository,
  `-v --daemon --pidfile <file> --remote root@<tailnet address>
  --ssh-cmd 'ssh -oStrictHostKeyChecking=accept-new -oServerAliveInterval=60'
  10.0.0.0/8`, plus `--dns`, plus the remote's IPv6 VPC prefix on the
  dual-stack remote. The firewall method was `auto`. With `auto`, sshuttle selected `nat`, the iptables NAT method, on this client. The row without `--dns` from the story was dropped. With `--dns`, the only addition is the capture of UDP 53 to the client's resolvers. The data plane is the same, so the numbers are the same within noise.
* One extra sshuttle row per remote with `--latency-buffer-size 4194304`, at
  the user's request, against the default of 32768 bytes.

The two remotes are both aarch64 burstable nodes with 2 vCPU in one VPC. Both run the bench server from `test/e2e/loss/bench` on their VPC addresses under `timeout`. The direct remote is in a public dual-stack subnet. The client reaches it over IPv6 without a relay.

The relayed remote is in a
private IPv4-only subnet behind a NAT instance. Tailscale found a direct path through that NAT instance, because the instance keeps endpoint-independent mappings. Thus, to force the relay for the test, `TS_DEBUG_ALWAYS_USE_DERP=true` was set in `/etc/default/tailscaled` on the remote. A managed NAT gateway forces the
relay by itself. The relay ceiling of that path is 22.3 Mbit/s, measured in
spike 6 with both tj transports and again here.

Per session, in this order:

1. A download of 60 s from the bench server over IPv4. The size is above the reachable rate, so the download always ends at the time box. During the download, these probes run every 5 s:

   * One DNS query to the VPC resolver.
   * One TCP connect to the resolver's port 53.
   * One `tailscale ping -c 1` with a 3 s limit.

   The DNS query goes over UDP through tj and over TCP through sshuttle, because sshuttle does not capture UDP to a resolver in the VPC.
2. 20 TCP connects to the resolver's port 53, idle.
3. 10 DNS queries with `dig +time=2 +tries=1`, idle.
4. One lookup of the remote's own private name through the system resolver. This lookup is the check of the DNS mode.
5. On the dual-stack remote, a download of 30 s over IPv6.

The user set a budget of 30 minutes for the campaign. Thus these are the deviations from the story's method:

* Two cycles instead of three.
* The reliability run is the 60 s download with probes, instead of a 10 min session.
* The campaign did not include a run without the download.
* The campaign did not include a request-reply loop.
* The campaign did not include a netem run.

In both cycles, the sessions to the relayed remote ran first, because its DERP setting is lost when the instance is replaced. In cycle 1 on the relayed remote, the sshuttle session ran after the SSH-transport session, because the first sshuttle start failed. The NOPASSWD rule that `sshuttle --sudoers-no-modify` generates contains `PYTHONPATH=.../site-packages/sshuttle`. The firewall helper runs with `PYTHONPATH=.../site-packages`. Thus sudo asked for a password that a job without a terminal cannot give. The user corrected the rule, and the session
ran.

The `tailscale ping` in the probe reports the tailnet path next to the tunnel probes. Thus a gap can be attributed to the path or to the tool.

The TCP connect number is not comparable between the tools. sshuttle accepts the connection on the client in 0.3 ms and opens the remote side later. tj completes the connect end to end in the round-trip time. The DNS query over TCP is the fair latency number for sshuttle. The tables contain that number.

## Result 1: bandwidth

The values are the rate of the bytes received in the time box, as cycle 1 / cycle 2.

| Configuration | Relayed, IPv4, 60 s | Direct, IPv4, 60 s | Direct, IPv6, 30 s |
|---------------|---------------------|--------------------|--------------------|
| tj QUIC | 22.3 / 22.3 Mbit/s | 552.7 / 523.3 Mbit/s | 554.0 / 550.0 Mbit/s |
| sshuttle, task flags | 8.1 / 8.1 Mbit/s | 11.0 / 10.9 Mbit/s | 10.9 / 11.0 Mbit/s |
| tj SSH transport | 22.2 / 20.8 Mbit/s | 68.4 / 55.3 Mbit/s | 55.3 / 55.9 Mbit/s |
| sshuttle, 4 MiB latency buffer, one cycle | 17.7 Mbit/s | 75.0 Mbit/s | 71.3 Mbit/s |

The relay ceiling is 22.3 Mbit/s, and both tj transports reach it. The rate of sshuttle with the task's flags is its latency buffer divided by the round-trip time. On the direct path, 32 KiB per 25 ms is 10.5 Mbit/s. On the relayed path, 32 KiB per 30 ms is 8.7 Mbit/s. With the 4 MiB buffer, the rate is 2.2 times higher on the relayed path and 6.8 times higher on the direct path. The cost of that buffer is in Result 3.

## Result 2: latency, idle

The values are from 20 TCP connects and 10 DNS queries in an idle tunnel, as cycle 1 / cycle 2. All connects and all queries succeeded.

| Configuration | Relayed, connect p50 / p95 | Relayed, DNS min to max | Direct, connect p50 / p95 | Direct, DNS min to max |
|---------------|----------------------------|-------------------------|---------------------------|------------------------|
| tj QUIC | 30.3 / 33.2, 27.7 / 28.6 ms | 28 to 43, 27 to 30 ms | 24.8 / 25.3, 24.7 / 25.1 ms | 25 to 27, 24 to 27 ms |
| sshuttle, task flags | local accept | 30 to 41, 28 to 34 ms | local accept | 25 to 31, 27 to 32 ms |
| tj SSH transport | 28.1 / 30.4, 28.5 / 29.9 ms | 27 to 34, 28 to 35 ms | 25.2 / 26.8, 25.3 / 27.9 ms | 25 to 29, 25 to 26 ms |
| sshuttle, 4 MiB latency buffer, one cycle | local accept | 29 to 60 ms | local accept | 27 to 30 ms |

In an idle tunnel, the answers arrive in the round-trip time for every configuration. The one outlier is
a connect maximum of 581 ms in cycle 1 of the SSH transport on the relayed
path, right after the download.

## Result 3: latency under load and reliability

The values are from the probes during the 60 s download, with 10 to 12 probes per session, as cycle 1 / cycle 2. Every DNS query and every TCP connect in every session got an answer. Thus the answered share is 100%, and the longest gap without an answer is 0 s. All sessions stayed up. The laptop's tailscaled journal contains 9 `now using` lines for the whole campaign, one per path discovery after a connect. The `tj-session` journal does not contain a warning.

| Configuration | Relayed, DNS min to max | Direct, DNS min to max | Disco ping timeouts |
|---------------|-------------------------|------------------------|---------------------|
| tj QUIC | 27 to 65, 30 to 74 ms | 25 to 58, 25 to 60 ms | 0 relayed, 4 direct |
| sshuttle, task flags | 31 to 100, 38 to 96 ms | 37 to 65, 30 to 62 ms | 0 |
| tj SSH transport | 29 to 795, 30 to 781 ms | 25 to 355, 26 to 218 ms | 0 |
| sshuttle, 4 MiB latency buffer, one cycle | 273 to 948 ms | 69 to 321 ms | 0 |

On the relayed path, the SSH transport queues a DNS query behind the bulk flow for up to 795 ms. On the direct path, the queue time is up to 355 ms. The reason is that every flow goes over one TCP connection with a 4 MiB yamux window. sshuttle with the 4 MiB buffer shows the same pattern, with a higher maximum. QUIC keeps the query within 50 ms of idle on both paths. sshuttle with the task's flags stays within 70 ms of idle, because its small buffer is not enough to fill the path.

During the QUIC downloads on the direct path, at 550 Mbit/s, four disco pings of 3 s timed out. The DNS and TCP probes of the same moments got their answers. The tunnel did not fail. The disco ping timed out because the UDP path was saturated. The flap detector of the session reads the status endpoint every 10 s and does not send a ping. Thus it is not affected.

## Result 4: the DNS modes

The system resolver answered the remote's own private name in every session. That is 8 of 8 tj sessions with `--dns all` and 6 of 6 sshuttle sessions with `--dns`.

## Result 5: the direct-path ceiling

The 550 Mbit/s of tj QUIC on the direct path is the limit of the client's link, not a limit of tj:

* An 8-stream iperf3 download from a public server reached 609 Mbit/s over
  the same WiFi link, so tj QUIC reaches about 90% of the link.
* During a tj QUIC download at 397 Mbit/s, the helper used 35% to 44% of one CPU on the remote. At the same time, 40% to 53% of the two vCPU were idle. The tj client used 30% to 59% of one CPU on the laptop, with 88% to 95% idle.
* Four downloads in parallel through tj summed to 322 Mbit/s, against 303 Mbit/s for one download in the same minute. Thus the limit of the rate is the path, not the per-flow controller.
* A single TCP download over the tailnet without tj, from the bench server on the remote's tailnet address, reached 159 to 177 Mbit/s. Four downloads in parallel summed to 152 Mbit/s. On the remote, the kernel's Cubic is sensitive to the small loss of the WiFi path. BBR in tj is not sensitive to that loss.

The WiFi rate varied between 303 and 554 Mbit/s across the evening, which is
the factor of 1.6 that was recorded in spike 5.

## Decisions

1. **QUIC stays the default transport, and the cutover from sshuttle
   to tj proceeds without an interim sshuttle tuning.** tj QUIC is better than sshuttle
   with the task's flags on every number on both paths. Its rate is 2.8 times higher on the relayed path and 50 times higher on the direct path. The idle latency is the same. The queueing under load is less. sshuttle with the 4 MiB buffer reaches 14%
   of the QUIC rate on the direct path at 5 times the DNS latency under load.
2. **Nothing changes in the gateway module of the infrastructure repository.** With tj, the relayed remote reaches the relay ceiling. The direct remote reaches the limit of the client's link. The remote settings are not a limit. Tailscale finds a direct path to a private-subnet remote behind a NAT instance. Behind a managed NAT gateway, Tailscale uses the relay. Both
   work.
3. **The SSH fallback's queueing is a recorded limit, not a change here.** Per the story, product changes are not allowed. A second SSH connection for the DNS, UDP, and ICMP flows would keep them out of the bulk queue. That is a candidate story with low priority. The reason is that the fallback applies only when the tailnet policy does not contain the UDP grant. A smaller yamux window is not the fix: in spike 6, the rate with 512 KiB was 76% lower under loss.
4. **The relay ceiling of 22.3 Mbit/s stays.** The tj settings do not change it.
   With the task's flags, sshuttle reaches 36% of it.
5. **The TCP connect number is not a comparison metric between the tools.**
   sshuttle accepts locally. The compare job keeps the number for tj and
   labels it for sshuttle.
