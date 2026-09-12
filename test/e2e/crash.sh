#!/usr/bin/env bash
# crash.sh proves the chunk 6 done-when: an interrupted session leaves no
# route, no device, no DNS change, and no file on the remote. It runs in the
# same rootless podman rig as run.sh, but as a separate script and a
# separate container, so it never interferes with a run.sh in progress.
#
# It connects with --dns none, so there is no DNS change to revert; the DNS
# chunk's own disconnect test covers a DNS revert on a real change.
#
# Scenario A sends SIGKILL to the tracked session process directly, the way
# an OOM kill or an operator's kill -9 would, and checks that systemd's
# ExecStopPost cleanup still removes the device, the routes, and the remote
# file, and that a second manual cleanup call is a no-op. Scenario B ends a
# session the normal way, with tj disconnect, which stops the unit with
# SIGTERM. Scenario C repeats the hard kill of scenario A on the ssh
# transport, where a session runs one helper process per lane, and checks
# that the crash clears every helper process and the remote cache, not only
# the single quic helper.
#
# Scenario A signals the tracked PID rather than running
# `systemctl kill -s SIGKILL tj-session`: measured on this rig, `systemctl
# kill` sends the signal to the whole unit cgroup as one administrative
# action and systemd then skips ExecStopPost, while a signal to the tracked
# process alone is treated as the process dying on its own, which does run
# ExecStopPost. The second form is what a real crash or an OOM kill looks
# like to systemd, so it is the meaningful test.
#
# It never runs tj connect on the host: the host holds its own session and
# the one-session rule forbids a second. Every tj invocation below goes
# through the rig.
#
# Real gateway values live in target.env (git-ignored) or TJ_TEST_* in the
# environment. Committed files use placeholders only.
set -euo pipefail

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$DIR/../.." && pwd)"

if [ -f "$DIR/target.env" ]; then
	set -a
	# shellcheck disable=SC1091
	. "$DIR/target.env"
	set +a
fi

: "${TJ_TEST_REF:?set TJ_TEST_REF (the connect reference, a hostname or tag) in target.env or the environment}"
: "${TJ_TEST_REMOTE:?set TJ_TEST_REMOTE (the gateway tailnet IPv4) in target.env or the environment}"
TJ_TEST_USER="${TJ_TEST_USER:-root}"
QUIC_PORTS="${TJ_TEST_QUIC_PORTS:-7443-7452}"
QUIC_PORT_RE='74(4[3-9]|5[0-2])'

TSGW_SOCK="/var/run/tailscale/tailscaled.sock"
IMAGE="tj-e2e:latest"
CONTAINER="tj-e2e-crash-$$"
SSH_OPTS=(-o StrictHostKeyChecking=accept-new -o ConnectTimeout=15 -o BatchMode=yes)

pass=0
fail=0

log() { printf '\n=== %s ===\n' "$*"; }
ok() { printf 'PASS: %s\n' "$*"; pass=$((pass + 1)); }
bad() { printf 'FAIL: %s\n' "$*"; fail=$((fail + 1)); }

rig() { podman exec "$CONTAINER" "$@"; }

# remote_quic_listeners prints the UDP listeners of the remote in the QUIC
# port range, one per line, as "address:port".
remote_quic_listeners() {
	ssh "${SSH_OPTS[@]}" "root@$1" 'ss -Hlun' 2>/dev/null | awk '{print $4}' | grep -E ":${QUIC_PORT_RE}\$" || true
}

# remote_helper_count prints the number of tj-helper processes on a remote.
# The bracket in the pattern keeps the ssh command's own process from
# matching itself.
remote_helper_count() {
	ssh "${SSH_OPTS[@]}" "root@$1" "pgrep -fc '[t]j-helper\.'" 2>/dev/null || true
}

cleanup() {
	log "cleanup"
	podman rm -f "$CONTAINER" >/dev/null 2>&1 || true
}
trap cleanup EXIT

# wait_gone polls a rig command until it fails (the thing it checks is
# gone), or bails out after the timeout in seconds.
wait_gone() {
	local timeout="$1"
	shift
	local waited=0
	while rig "$@" >/dev/null 2>&1; do
		waited=$((waited + 1))
		if [ "$waited" -ge "$timeout" ]; then
			return 1
		fi
		sleep 1
	done
	return 0
}

# wait_present polls a rig command until it succeeds, or bails out after the
# timeout in seconds. It steadies a check against the brief window right
# after connect where a route or a device is still being set up.
wait_present() {
	local timeout="$1"
	shift
	local waited=0
	while ! rig "$@" >/dev/null 2>&1; do
		waited=$((waited + 1))
		if [ "$waited" -ge "$timeout" ]; then
			return 1
		fi
		sleep 1
	done
	return 0
}

has_tj0_route() {
	# Capture first, then match: piping straight into grep -q races under
	# pipefail, because grep can close its end of the pipe as soon as it
	# has its match, and the SIGPIPE that follows makes the upstream rig
	# command look like it failed even though it printed a match.
	local routes
	routes="$(rig sh -c 'ip route show; ip -6 route show')"
	case "$routes" in
	*'dev tj0'*) return 0 ;;
	*) return 1 ;;
	esac
}

# wait_tj0_route polls has_tj0_route until it matches present (true) or
# absent (false), or bails out after the timeout in seconds.
wait_tj0_route() {
	local want="$1" timeout="$2" waited=0
	while true; do
		if [ "$want" = present ] && has_tj0_route; then return 0; fi
		if [ "$want" = absent ] && ! has_tj0_route; then return 0; fi
		waited=$((waited + 1))
		if [ "$waited" -ge "$timeout" ]; then
			return 1
		fi
		sleep 1
	done
}

no_tj0_routes() {
	! has_tj0_route
}

no_runtime_files() {
	! rig sh -c 'test -e /run/tj/session.json -o -e /run/tj/plan.json'
}

no_remote_file() {
	local left
	left="$(ssh "${SSH_OPTS[@]}" "root@${TJ_TEST_REMOTE}" \
		'ls -A "$HOME/.cache/tj" 2>/dev/null; ls -A /run/user/0/tj-helper.* 2>/dev/null' || true)"
	[ -z "$left" ]
}

assert_clean() {
	local label="$1"
	if wait_gone 15 ip link show tj0; then
		ok "$label: tj0 device is gone"
	else
		bad "$label: tj0 device still exists"
	fi
	if no_tj0_routes; then
		ok "$label: no dev tj0 route remains"
	else
		bad "$label: a dev tj0 route remains"
		rig sh -c 'ip route show; ip -6 route show' | grep 'dev tj0' || true
	fi
	if rig ip rule show | grep -q 'lookup 117' || rig ip -6 rule show | grep -q 'lookup 117'; then
		bad "$label: a session rule remains"
	else
		ok "$label: no session rule remains"
	fi
	if [ -z "$(rig ip route show table 117 2>/dev/null)$(rig ip -6 route show table 117 2>/dev/null)" ]; then
		ok "$label: the session table is empty"
	else
		bad "$label: the session table still has routes"
	fi
	if no_runtime_files; then
		ok "$label: no local runtime file remains"
	else
		bad "$label: a local runtime file remains"
	fi
	if no_remote_file; then
		ok "$label: no file remains on the remote"
	else
		bad "$label: a file remains on the remote"
	fi
}

# The host may run its own tj session; the guard at the end compares the
# host's tj0 routes before and after the rig, so a live host session is not
# a failure.
host_tj0_before="$(ip route show 2>/dev/null | grep 'dev tj0' || true)"

log "build tj (host arch) with the embedded helpers"
(cd "$ROOT" && task build)

log "build the rig image"
podman build -t "$IMAGE" -f "$DIR/Containerfile" "$DIR"

log "start the rig"
podman run -d --name "$CONTAINER" \
	--systemd=always \
	--device /dev/net/tun \
	--cap-add NET_ADMIN --cap-add NET_RAW --cap-add SYS_ADMIN \
	-v "${TSGW_SOCK}:${TSGW_SOCK}" \
	-v "${ROOT}/bin/tj:/usr/local/bin/tj:ro" \
	"$IMAGE" >/dev/null

log "wait for systemd in the rig"
for _ in $(seq 1 30); do
	if rig systemctl is-system-running >/dev/null 2>&1; then break; fi
	sleep 1
done
rig systemctl is-system-running || true

log "scenario A: tj connect, then a hard kill of the session process"
rig tj -v connect "$TJ_TEST_REF" --user "$TJ_TEST_USER" --dns none

if wait_present 5 ip link show tj0; then
	ok "session up: tj0 exists"
else
	bad "session up: tj0 does not exist"
fi
if wait_tj0_route present 5; then
	ok "session up: at least one dev tj0 route exists"
else
	bad "session up: no dev tj0 route exists"
	rig sh -c 'ip route show; ip -6 route show'
fi

pid="$(rig systemctl show tj-session -p MainPID --value)"
if [ -z "$pid" ] || [ "$pid" = "0" ]; then
	printf 'FATAL: could not read the tj-session MainPID\n'
	exit 1
fi
ok "scenario A: read the session's MainPID ($pid)"
rig kill -9 "$pid"

log "scenario A: confirm the ExecStopPost cleanup ran"
assert_clean "scenario A"

journal="$(rig journalctl -u tj-session --no-pager 2>/dev/null || true)"
case "$journal" in
*'cleanup complete'*) ok "scenario A: the journal has the cleanup log line" ;;
*) bad "scenario A: the journal has no cleanup log line" ;;
esac
printf '%s\n' "$journal" | tail -n 40

log "confirm a second cleanup call is a no-op"
if rig tj _session cleanup; then
	ok "first manual cleanup call exits 0"
else
	bad "first manual cleanup call failed"
fi
if rig tj _session cleanup; then
	ok "second manual cleanup call exits 0"
else
	bad "second manual cleanup call failed"
fi
assert_clean "manual cleanup"

log "scenario B: tj connect, then a normal tj disconnect (SIGTERM)"
rig tj -v connect "$TJ_TEST_REF" --user "$TJ_TEST_USER" --dns none

if wait_present 5 ip link show tj0; then
	ok "session up: tj0 exists"
else
	bad "session up: tj0 does not exist"
fi

rig tj disconnect

log "scenario B: confirm disconnect reverted everything"
assert_clean "scenario B"

status_after="$(rig tj status)"
case "$status_after" in
*'no active session'*) ok "scenario B: tj status shows no active session" ;;
*) bad "scenario B: tj status still shows a session: ${status_after}" ;;
esac

journal="$(rig journalctl -u tj-session --no-pager 2>/dev/null || true)"
case "$journal" in
*'session down'*) ok "scenario B: the journal has the graceful shutdown log line" ;;
*) bad "scenario B: the journal has no graceful shutdown log line" ;;
esac

log "scenario C: tj connect --transport ssh --dns none, then a hard kill of the session process"
rig tj -v connect "$TJ_TEST_REF" --user "$TJ_TEST_USER" --dns none --transport ssh

if wait_present 5 ip link show tj0; then
	ok "scenario C: session up: tj0 exists"
else
	bad "scenario C: session up: tj0 does not exist"
fi
if wait_tj0_route present 5; then
	ok "scenario C: session up: at least one dev tj0 route exists"
else
	bad "scenario C: session up: no dev tj0 route exists"
	rig sh -c 'ip route show; ip -6 route show'
fi

HELPERS="$(remote_helper_count "$TJ_TEST_REMOTE")"
if [ "$HELPERS" -eq 3 ]; then
	ok "scenario C: the remote has 3 helper processes"
else
	bad "scenario C: the remote has ${HELPERS} helper processes, want 3"
fi

pid="$(rig systemctl show tj-session -p MainPID --value)"
if [ -z "$pid" ] || [ "$pid" = "0" ]; then
	printf 'FATAL: could not read the tj-session MainPID\n'
	exit 1
fi
ok "scenario C: read the session's MainPID ($pid)"
rig kill -9 "$pid"

log "scenario C: confirm the ExecStopPost cleanup ran"
assert_clean "scenario C"

HELPERS="$(remote_helper_count "$TJ_TEST_REMOTE")"
if [ "$HELPERS" -eq 0 ]; then
	ok "scenario C: no helper process remains on the remote"
else
	bad "scenario C: ${HELPERS} helper processes remain on the remote"
fi
LISTENERS="$(remote_quic_listeners "$TJ_TEST_REMOTE")"
if [ -z "$LISTENERS" ]; then
	ok "scenario C: no udp listener in ${QUIC_PORTS} remains on the remote"
else
	bad "scenario C: udp listeners remain on the remote: ${LISTENERS}"
fi

log "confirm the rig left no tj0 route on the host"
host_tj0_after="$(ip route show 2>/dev/null | grep 'dev tj0' || true)"
if [ "$host_tj0_after" = "$host_tj0_before" ]; then
	ok "the host's tj0 routes are unchanged by the rig"
else
	bad "the host's tj0 routes changed; the rig must not affect the host"
	printf 'before:\n%s\nafter:\n%s\n' "$host_tj0_before" "$host_tj0_after"
fi

log "result: ${pass} passed, ${fail} failed"
[ "$fail" -eq 0 ]
