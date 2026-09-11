# CLAUDE.md

## Project

tailjump, binary `tj`, gives an engineer a session into a remote network over Tailscale SSH. Read `docs/spec.md` and `docs/architecture.md` before a change. The spec is Linear story K8S-194. Each chunk is one story and one PR.

## Rules

1. Commit messages and PR titles follow Conventional Commits. PRs are squash-merged.
2. Never run `tj connect` on the host. The developer laptop has an sshuttle session, and one session per machine is the rule. Use `task e2e`, which runs the podman rig from `test/e2e`.
3. The shared gateway `shared-gateway` is the only remote for tests. Do not use any other gateway.
4. Never leave a file on a remote. After a test, check with `ssh root@100.64.0.10 'ls -la /root/.cache/tj /run/user/0 2>&1'`.
5. Add a comment only when the code cannot explain itself. Keep it short. No em dashes.
6. Docs, commit messages, and PR bodies use plain verbs and complete sentences. No metaphors, no em dashes.
7. `cmd/tjhelper` imports no package that imports `tailscale.com`, cobra, or gvisor.
8. Run `task lint` and `task test` before a commit.

## Tools

`mise install` installs go, golangci-lint, goreleaser, and task. Tasks: `task build`, `task helpers`, `task test`, `task lint`, `task e2e`.

## Public repository hygiene

1. Committed code, tests, and docs use placeholder addresses only, for example `gw.example`, `10.0.0.0/16`, `2001:db8::/56`, and `tag:example`.
2. The real test gateway lives in `test/e2e/target.env` (git-ignored) and in `TJ_TEST_*` environment variables. Read the target from those, never from a hardcoded value.
3. Do not name a customer in any committed file.
