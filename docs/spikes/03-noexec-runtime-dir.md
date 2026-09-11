# Spike 3: remote with a noexec runtime directory

## Goal

Confirm the helper upload path works when the runtime directory is absent or mounted noexec, and pick the exec directory.

## Result

- On the shared gateway over Tailscale SSH, `XDG_RUNTIME_DIR` is unset, `HOME` is `/root`, and `/root/.cache` does not exist. The upload command creates `/root/.cache/tj`. No mount there is noexec.
- On a noexec mount, a direct exec of a script and of a static binary both fail with `EACCES`. A write and a `chmod` still succeed, so they are not sufficient signals.
- A shell script still runs on a noexec mount, because the interpreter reads it. So the exec-time probe must run a binary, not a script.
- A `memfd` loader runs a binary on a noexec-only remote, because the anonymous file descriptor is off the mount. It needs a non-shell delivery path, so it is out of scope for v1 and recorded as a known option.

## Decision

The exec-directory order is `$XDG_RUNTIME_DIR` then `$HOME/.cache/tj`. On this gateway the second path is the one used. The client probe must confirm execution, not only a write.
