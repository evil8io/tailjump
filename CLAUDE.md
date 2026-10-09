# CLAUDE.md

## Project

tailjump, binary `tj`, creates a point-to-site VPN tunnel to a Tailscale peer over Tailscale SSH. Read `docs/spec.md` and `docs/architecture.md` before a change.

## Rules

1. Commit messages and PR titles follow Conventional Commits. PRs are squash-merged.
2. On the host, run no tj command that starts, stops, or replaces a session, or that contacts a remote. The host has the user's live tj session, and one session per machine is the rule. Use `task e2e`, which runs the podman rig from `test/e2e` with the host's tailscaled socket only. Do not start `task e2e:compare`, because it runs tj on the host. Only the user starts it.
3. Test only against the remotes in `test/e2e/target.env`. Use the DERP-relayed remote of `TJ_TEST_DERP_REF` for the relayed-path scenario only. Set `TJ_TEST_REF` to a hostname when other online gateways have the same tag. Resolution picks the tag match with the most recent WireGuard handshake, and a gateway with other networks fails the checks.
4. Never leave a file on a remote. After a test, check with `. test/e2e/target.env && ssh root@"$TJ_TEST_REMOTE" 'ls -la /root/.cache/tj /run/user/0 2>&1'`. A failed helper upload leaves a partial `tj-helper.<pid>` file. Remove it before the next run. A helper removes only files older than 10 minutes, and the cache directory checks of the rig fail before that.
5. Run one rig script at a time. Start it in the background, because a run takes 2 to 26 minutes, measured on 2026-10-04, and a command timeout is shorter. A run counts the helper processes, files, and listeners on the remote, so a second run at the same time breaks those checks. A run of `run.sh` also writes a temporary manifest on the gateway, and a second run can then restore the wrong manifest.
6. Do not edit a script in `test/e2e` while a run of that script is active. Bash reads a script file while it runs, so an edit changes the commands of the active run.
7. Set `XDG_CONFIG_HOME` to a scratch directory in the same shell command as a test of `tj config` or `tj alias` on the host. An environment variable from an earlier shell call does not carry over. The write then replaces the user's `~/.config/tj/config.yaml`.
8. Add a comment only when the code cannot explain itself. Keep it short. No em dashes.
9. Write docs, commit messages, and PR bodies with plain verbs and complete sentences. Do not use metaphors or em dashes.
10. Keep the README short and plain, with no marketing text and no "engineer" or "customer". Describe tj in the README as a point-to-site VPN tunnel to a Tailscale peer, because tj works in any tailnet. The maintainer chose that wording.
11. Import no package in `cmd/tjhelper` that depends on `tailscale.com`, cobra, gvisor, or `log/slog`. Keep each helper binary under 8 MiB; `task helpers` fails at or above that size. The allowed imports and the sizes are in `docs/architecture.md`, "Build", item 5.
12. Run `task lint` and `task test` before a commit.
13. Keep the two `enabled: false` rules in `renovate.json`. Renovate cannot update the quic-go fork or gvisor, and the reason for each is in its rule description. With `group:allNonMajor`, every non-major update is in one PR, so one dependency that fails `go get` blocks every other update. Bump the fork by hand as its rule description says. Bump gvisor with `go get gvisor.dev/gvisor@go`, then run `go mod tidy`.
14. A release-please PR has no CI checks, because the action pushes with `GITHUB_TOKEN`, and GitHub starts no workflow for that push. `gh pr checks` then prints `no checks reported` and exits 1, which is no test failure.

## Tools

`mise install` installs go, golangci-lint, goreleaser, and task. Tasks: `task build`, `task helpers`, `task test`, `task lint`, `task fmt`, `task e2e`, `task e2e:crash`, `task e2e:bench`, `task e2e:loss`, `task e2e:metrics`, and `task e2e:compare`. No task runs `test/e2e/reconnect.sh`. Run it with `bash test/e2e/reconnect.sh`.

## Public repository hygiene

1. Use placeholder addresses only in committed code, tests, and docs, for example `gw.example`, `10.0.0.0/16`, `2001:db8::/56`, and `tag:example`.
2. The real test gateway is in `test/e2e/target.env` (git-ignored) and in `TJ_TEST_*` environment variables. Read the target from those, never from a hardcoded value.
3. Do not name a customer, a private repository, an internal environment, or an issue tracker ID in any committed file, because the repository is public.
4. Do not commit a real hostname, a real tailnet address, a real VPC address, or a real resolver address.
5. Do not commit a DERP region code, a cloud account ID, or a security group ID. Use the DERP region code `xyz` in test fixtures.
