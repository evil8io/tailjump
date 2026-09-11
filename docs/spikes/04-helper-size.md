# Spike 4: helper size

## Goal

Keep the uploaded helper small, because the client uploads it every session over the SSH channel.

## Result

Small enough. The helper is well under the 8 MiB target.

| Build | linux/arm64 | linux/amd64 |
| -- | -- | -- |
| `-trimpath -ldflags "-s -w"` | 2.25 MiB | 2.30 MiB |
| default | 4.65 MiB | 4.87 MiB |

- The helper imports no `encoding/json` and no `log/slog`. Both pull weight for no benefit. The control line uses `fmt.Appendf` and `%q`, and the client parses it with `strconv`.
- yamux is 1.5% of the binary.
- The helper imports no package that imports `tailscale.com`, cobra, or gvisor.
- Upload takes 291 ms, which is 90 Mbit/s. `upx` was not available, so there is no compressed figure.

The release build uses `-trimpath -ldflags "-s -w"`.
