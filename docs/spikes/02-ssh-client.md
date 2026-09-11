# Spike 2: Go SSH client against Tailscale SSH

## Goal

Confirm that a Go SSH client reaches the gateway over Tailscale SSH, runs a command, and uploads and runs the helper.

## Result

Viable. The `golang.org/x/crypto/ssh` client connects, runs commands, and carries the mux over one exec channel.

- Auth `none` works. The client sends a `none` probe before it reads the auth methods, and Tailscale SSH accepts it. A non-nil host-key callback is still required.
- The banner is empty in the normal accept case. Check mode is off on this tailnet, so the SSH channel that carries the check-mode sign-in URL is unverified. The client prints the banner and any keyboard-interactive prompt.
- The host key is stable across connections, type `ecdsa-sha2-nistp256`.
- The keepalive reply is `ok=false` with a nil error. That is a completed round trip and a valid liveness signal.
- Tailscale SSH runs a command as `/bin/bash -c`, not a login shell. It accepts no `SetEnv`, so variables go inside the command string. A non-zero exit is an `*ssh.ExitError`.
- Two channels on one connection work. The client uploaded the helper on one channel and ran it on another. `cat > file` returns only at EOF, so the client closes the stdin pipe before it waits.
- The local API returns the peers over the unix socket with `net/http`. Under `Peer` the keys are HostName, Online, TailscaleIPs, and Tags; Tags is null when the peer is untagged.

## Pending

The check-mode URL channel needs a tailnet with check mode on to verify.
