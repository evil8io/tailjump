#!/usr/bin/env bash
# bench.sh proves that tj bench measures the throughput of the transport, up
# and then down, without a session and without root. It checks the quic
# transport, the ssh transport, the --json form, the --duration range, the
# fallback when the quic port range is blocked, a bench beside an active
# session, an interrupt, and that every run and every disconnect leaves no
# file, process, or listener on the remote.
#
# It runs in its own rig, separate from run.sh and crash.sh, so it never
# interferes with a run of either in progress.
#
# It never runs tj connect on the host: the host holds its own session and
# the one-session rule forbids a second. Every tj invocation below goes
# through the rig. It never writes or removes /etc/tj on the remote.
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

command -v jq >/dev/null 2>&1 || {
	printf 'FATAL: jq is required on the host to check tj bench --json\n'
	exit 1
}

TSGW_SOCK="/var/run/tailscale/tailscaled.sock"
IMAGE="tj-e2e:latest"
CONTAINER="tj-e2e-bench-$$"
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
# The anchor keeps the tailscaled ssh incubator, whose command line quotes
# the exec command, and the ssh command's own shell from matching.
remote_helper_count() {
	ssh "${SSH_OPTS[@]}" "root@$1" "pgrep -fc '^/[^ ]*/tj-helper\.'" 2>/dev/null || true
}

# remote_cache_listing prints the remote's tj cache directory. Empty output
# means clean.
remote_cache_listing() {
	ssh "${SSH_OPTS[@]}" "root@$1" 'ls -A "$HOME/.cache/tj" 2>/dev/null' || true
}

# remote_run_files prints a leftover helper file under /run/user/0 on a
# remote. Empty output means clean.
remote_run_files() {
	ssh "${SSH_OPTS[@]}" "root@$1" 'ls -A /run/user/0/tj-helper.* 2>/dev/null' || true
}

# block_quic_range drops the helper's replies on the input hook, so the
# client's handshake times out the way it does when the tailnet policy has no
# UDP rule. A drop on the output hook would fail the send at once with EPERM
# and skip the timeout path.
block_quic_range() {
	rig nft add table inet tjtest
	rig nft add chain inet tjtest input '{ type filter hook input priority 0; }'
	rig nft add rule inet tjtest input udp sport "${QUIC_PORTS}" drop
}

unblock_quic_range() {
	rig nft delete table inet tjtest >/dev/null 2>&1 || true
}

# bench_row prints one row of a tj bench table by its label, with the label
# and its padding stripped, for example "212 mbps (127 MiB in 5.0s)" for
# label Up.
bench_row() {
	printf '%s\n' "$1" | grep -E "^$2:" | sed -E "s/^$2:[[:space:]]+//"
}

# bench_rate prints the leading number of a tj bench Up: or Down: row.
bench_rate() {
	printf '%s' "$1" | awk '{print $1}'
}

# remote_clean reports whether a remote has no helper process, no cache
# entry, no run file, and no udp listener in the QUIC range.
remote_clean() {
	local helpers cache runfiles listeners
	helpers="$(remote_helper_count "$1")"
	cache="$(remote_cache_listing "$1")"
	runfiles="$(remote_run_files "$1")"
	listeners="$(remote_quic_listeners "$1")"
	[ "${helpers:-0}" -eq 0 ] && [ -z "$cache" ] && [ -z "$runfiles" ] && [ -z "$listeners" ]
}

# wait_remote_clean polls remote_clean for up to 10 s, so a helper that is
# still exiting does not fail the check.
wait_remote_clean() {
	local waited=0
	while ! remote_clean "$1"; do
		waited=$((waited + 1))
		if [ "$waited" -ge 10 ]; then
			return 1
		fi
		sleep 1
	done
	return 0
}

# assert_remote_clean reports the remote_clean result for a labelled check,
# and prints the leftovers on a failure.
assert_remote_clean() {
	local label="$1" addr="$2"
	if wait_remote_clean "$addr"; then
		ok "${label}: the remote has no helper, no cache entry, no run file, and no udp listener"
	else
		bad "${label}: the remote is not clean"
		printf 'helpers=%s cache=%s runfiles=%s listeners=%s\n' \
			"$(remote_helper_count "$addr")" "$(remote_cache_listing "$addr")" \
			"$(remote_run_files "$addr")" "$(remote_quic_listeners "$addr")"
	fi
}

cleanup() {
	log "cleanup"
	unblock_quic_range
	podman rm -f "$CONTAINER" >/dev/null 2>&1 || true
}
trap cleanup EXIT

log "build tj (host arch) with the embedded helpers"
(cd "$ROOT" && task build)

log "build the rig image"
podman build -t "$IMAGE" -f "$DIR/Containerfile" "$DIR"

log "start the rig"
podman run -d --name "$CONTAINER" \
	--systemd=always \
	--dns-search=. \
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

log "check 1: tj bench on the quic transport"
code=0
BENCH1="$(rig timeout 180 tj bench "$TJ_TEST_REF" --user "$TJ_TEST_USER" --duration 2s)" || code=$?
printf '%s\n' "$BENCH1"
if [ "$code" -eq 0 ]; then
	ok "check 1: tj bench exits 0"
else
	bad "check 1: tj bench exited ${code}, want 0"
fi
TRANSPORT1="$(bench_row "$BENCH1" Transport)"
if printf '%s' "$TRANSPORT1" | grep -qE "^quic \(port ${QUIC_PORT_RE}\)\$"; then
	ok "check 1: transport is ${TRANSPORT1}"
else
	bad "check 1: transport is '${TRANSPORT1}', want quic with a port from ${QUIC_PORTS}"
fi
UP1="$(bench_row "$BENCH1" Up)"
DOWN1="$(bench_row "$BENCH1" Down)"
PATHROW1="$(bench_row "$BENCH1" Path)"
if [ -n "$UP1" ] && [ "$(bench_rate "$UP1")" -gt 0 ]; then
	ok "check 1: up = ${UP1}"
else
	bad "check 1: up row missing or zero: '${UP1}'"
fi
if [ -n "$DOWN1" ] && [ "$(bench_rate "$DOWN1")" -gt 0 ]; then
	ok "check 1: down = ${DOWN1}"
else
	bad "check 1: down row missing or zero: '${DOWN1}'"
fi
if [ -n "$PATHROW1" ]; then
	ok "check 1: path = ${PATHROW1}"
else
	bad "check 1: no path row"
fi
assert_remote_clean "check 1" "$TJ_TEST_REMOTE"

log "check 2: tj bench on the ssh transport"
code=0
BENCH2="$(rig timeout 180 tj bench "$TJ_TEST_REF" --user "$TJ_TEST_USER" --duration 2s --transport ssh)" || code=$?
printf '%s\n' "$BENCH2"
if [ "$code" -eq 0 ]; then
	ok "check 2: tj bench --transport ssh exits 0"
else
	bad "check 2: tj bench --transport ssh exited ${code}, want 0"
fi
TRANSPORT2="$(bench_row "$BENCH2" Transport)"
if printf '%s' "$TRANSPORT2" | grep -qE '^ssh'; then
	ok "check 2: transport is ${TRANSPORT2}"
else
	bad "check 2: transport is '${TRANSPORT2}', want ssh"
fi
UP2="$(bench_row "$BENCH2" Up)"
DOWN2="$(bench_row "$BENCH2" Down)"
if [ -n "$UP2" ] && [ "$(bench_rate "$UP2")" -gt 0 ]; then
	ok "check 2: up = ${UP2}"
else
	bad "check 2: up row missing or zero: '${UP2}'"
fi
if [ -n "$DOWN2" ] && [ "$(bench_rate "$DOWN2")" -gt 0 ]; then
	ok "check 2: down = ${DOWN2}"
else
	bad "check 2: down row missing or zero: '${DOWN2}'"
fi
assert_remote_clean "check 2" "$TJ_TEST_REMOTE"

log "check 3: tj bench --json"
code=0
JSON3="$(rig timeout 180 tj bench "$TJ_TEST_REF" --user "$TJ_TEST_USER" --duration 2s --json)" || code=$?
printf '%s\n' "$JSON3"
if [ "$code" -eq 0 ]; then
	ok "check 3: tj bench --json exits 0"
else
	bad "check 3: tj bench --json exited ${code}, want 0"
fi
if printf '%s' "$JSON3" | jq -e '.transport | length > 0' >/dev/null 2>&1; then
	ok "check 3: .transport = $(printf '%s' "$JSON3" | jq -r '.transport')"
else
	bad "check 3: .transport is missing or empty"
fi
if printf '%s' "$JSON3" | jq -e '.up.bytes > 0' >/dev/null 2>&1; then
	ok "check 3: .up.bytes > 0"
else
	bad "check 3: .up.bytes is not above 0"
fi
if printf '%s' "$JSON3" | jq -e '.down.bytes > 0' >/dev/null 2>&1; then
	ok "check 3: .down.bytes > 0"
else
	bad "check 3: .down.bytes is not above 0"
fi
if printf '%s' "$JSON3" | jq -e '.up.rate > 0' >/dev/null 2>&1; then
	ok "check 3: .up.rate > 0"
else
	bad "check 3: .up.rate is not above 0"
fi
if printf '%s' "$JSON3" | jq -e '.down.rate > 0' >/dev/null 2>&1; then
	ok "check 3: .down.rate > 0"
else
	bad "check 3: .down.rate is not above 0"
fi
if printf '%s' "$JSON3" | jq -e '.up.seconds >= 2' >/dev/null 2>&1; then
	ok "check 3: .up.seconds >= 2"
else
	bad "check 3: .up.seconds is below 2"
fi
if printf '%s' "$JSON3" | jq -e '.path.type | length > 0' >/dev/null 2>&1; then
	ok "check 3: .path.type = $(printf '%s' "$JSON3" | jq -r '.path.type')"
else
	bad "check 3: .path.type is missing or empty"
fi

log "check 4: tj bench refuses an out-of-range duration at once"
for BAD_DURATION in 31s 500ms; do
	start_ns="$(date +%s%N)"
	code=0
	OUT4="$(rig tj bench "$TJ_TEST_REF" --user "$TJ_TEST_USER" --duration "$BAD_DURATION" 2>&1)" || code=$?
	end_ns="$(date +%s%N)"
	elapsed="$(awk -v s="$start_ns" -v e="$end_ns" 'BEGIN { printf "%.1f", (e - s) / 1e9 }')"
	printf '%s\n' "$OUT4"
	if [ "$code" -eq 2 ]; then
		ok "check 4: --duration ${BAD_DURATION} exits 2"
	else
		bad "check 4: --duration ${BAD_DURATION} exited ${code}, want 2"
	fi
	if awk -v e="$elapsed" 'BEGIN { exit !(e < 3.0) }'; then
		ok "check 4: --duration ${BAD_DURATION} returned in ${elapsed}s, no connection opened"
	else
		bad "check 4: --duration ${BAD_DURATION} took ${elapsed}s, want a fast usage-error return"
	fi
done

log "check 5: quic blocked in the rig, auto falls back and quic fails"
block_quic_range
rig nft list table inet tjtest
code=0
BENCH5="$(rig timeout 180 tj bench "$TJ_TEST_REF" --user "$TJ_TEST_USER" --duration 2s --transport auto)" || code=$?
printf '%s\n' "$BENCH5"
if [ "$code" -eq 0 ]; then
	ok "check 5: --transport auto exits 0 with the range blocked"
else
	bad "check 5: --transport auto exited ${code}, want 0"
fi
TRANSPORT5="$(bench_row "$BENCH5" Transport)"
if printf '%s' "$TRANSPORT5" | grep -qE '^ssh \(fallback: .+\)$'; then
	ok "check 5: transport is ${TRANSPORT5}"
else
	bad "check 5: transport is '${TRANSPORT5}', want ssh with a fallback reason"
fi

code=0
OUT5="$(rig timeout 180 tj bench "$TJ_TEST_REF" --user "$TJ_TEST_USER" --duration 2s --transport quic 2>&1)" || code=$?
printf '%s\n' "$OUT5"
if [ "$code" -eq 1 ]; then
	ok "check 5: --transport quic exits 1 with the range blocked"
else
	bad "check 5: --transport quic exited ${code}, want 1"
fi

unblock_quic_range
assert_remote_clean "check 5" "$TJ_TEST_REMOTE"

log "check 6: tj bench beside an active session"
rig tj -v connect "$TJ_TEST_REF" --user "$TJ_TEST_USER" --dns none
INVOCATION_BEFORE="$(rig systemctl show -p InvocationID tj-session --value)"

code=0
BENCH6="$(rig timeout 180 tj bench "$TJ_TEST_REF" --user "$TJ_TEST_USER" --duration 2s)" || code=$?
printf '%s\n' "$BENCH6"
if [ "$code" -eq 0 ]; then
	ok "check 6: tj bench exits 0 beside an active session"
else
	bad "check 6: tj bench exited ${code}, want 0"
fi

STATUS6="$(rig tj status)"
printf '%s\n' "$STATUS6"
if printf '%s' "$STATUS6" | grep -qE '^Status:[[:space:]]+up'; then
	ok "check 6: tj status still reports up"
else
	bad "check 6: tj status does not report up"
fi
INVOCATION_AFTER="$(rig systemctl show -p InvocationID tj-session --value)"
if [ "$INVOCATION_AFTER" = "$INVOCATION_BEFORE" ]; then
	ok "check 6: the session unit kept its invocation"
else
	bad "check 6: the session unit restarted (invocation ${INVOCATION_BEFORE} -> ${INVOCATION_AFTER})"
fi
if printf '%s' "$STATUS6" | grep -q '^Reconnects:'; then
	bad "check 6: tj status has a Reconnects row"
else
	ok "check 6: tj status has no Reconnects row"
fi

rig tj disconnect
sleep 2
assert_remote_clean "check 6" "$TJ_TEST_REMOTE"

log "check 7: an interrupted tj bench exits 130"
code=0
rig timeout --preserve-status -s INT 3 tj bench "$TJ_TEST_REF" --user "$TJ_TEST_USER" --duration 10s || code=$?
if [ "$code" -eq 130 ]; then
	ok "check 7: the interrupted tj bench exits 130"
else
	bad "check 7: the interrupted tj bench exited ${code}, want 130"
fi
assert_remote_clean "check 7" "$TJ_TEST_REMOTE"

if [ -n "${TJ_TEST_DERP_REF:-}" ]; then
	: "${TJ_TEST_DERP_REMOTE:?set TJ_TEST_DERP_REMOTE in target.env or the environment}"
	log "check 9: tj bench against the relayed remote"
	code=0
	BENCH9="$(rig timeout 180 tj bench "$TJ_TEST_DERP_REF" --user "$TJ_TEST_USER" --duration 2s)" || code=$?
	printf '%s\n' "$BENCH9"
	if [ "$code" -eq 0 ]; then
		ok "check 9: tj bench against the relayed remote exits 0"
	else
		bad "check 9: tj bench against the relayed remote exited ${code}, want 0"
	fi
	assert_remote_clean "check 9" "$TJ_TEST_DERP_REMOTE"
fi

log "result: ${pass} passed, ${fail} failed"
[ "$fail" -eq 0 ]
