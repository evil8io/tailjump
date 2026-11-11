# Spike 4: helper size

## Goal

Keep the uploaded helper small, because the client uploads it every session over the SSH channel.

## Result

Small enough. The helper is well under the 8 MiB target.

| Build | linux/arm64 | linux/amd64 |
| -- | -- | -- |
| `-trimpath -ldflags "-s -w"` | 2.25 MiB | 2.30 MiB |
| default | 4.65 MiB | 4.87 MiB |

- The helper does not import `encoding/json` or `log/slog`. With them, the binary is larger without a benefit. The helper writes the control line with `fmt.Appendf` and `%q`. The client parses the control line with `strconv`.
- yamux is 1.5% of the binary.
- The helper does not import a package that imports `tailscale.com`, cobra, or gvisor.
- The upload takes 291 ms, at 90 Mbit/s. `upx` was not available, so this result does not contain a compressed figure.

The release build uses `-trimpath -ldflags "-s -w"`.
