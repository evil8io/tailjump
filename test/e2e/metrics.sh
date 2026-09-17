#!/usr/bin/env bash
# metrics.sh proves the live session metrics of tj status: the RTT of the
# transport, the latency of the tailscale path, the traffic rates and
# totals, the 5 s sample interval, and the atomic state file write. It runs
# in its own rootless podman rig and its own container, next to run.sh,
# crash.sh, and reconnect.sh, and never in the same container as them.
#
# It runs checks 1 to 6 once per transport, quic and ssh, on a session that
# connects with --dns none. Check 7 runs once, on the QUIC transport only,
# and repeats the helper-kill method of reconnect.sh's scenario 1 to prove
# that the byte totals and the RTT survive a reconnect. With TJ_TEST_DERP_REF
# set, it also connects once to the relayed remote and checks the metrics
# rows there.
#
# jq is not in the rig image. Every check parses tj status --json on the
# host, after a rig call prints it. This script never writes or removes
# /etc/tj on the remote, and it never runs tj on the host: every tj
# invocation goes through rig.
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
CONTAINER="tj-e2e-metrics-$$"
SSH_OPTS=(-o StrictHostKeyChecking=accept-new -o ConnectTimeout=15 -o BatchMode=yes)

# The minimum bytes the counters check's ping adds to each total: 40 pings
# of a 1200 byte payload.
MIN_PING_BYTES=$((40 * 1200))

pass=0
fail=0

log() { printf '\n=== %s ===\n' "$*"; }
ok() { printf 'PASS: %s\n' "$*"; pass=$((pass + 1)); }
bad() { printf 'FAIL: %s\n' "$*"; fail=$((fail + 1)); }

rig() { podman exec "$CONTAINER" "$@"; }

remote_ssh() {
	local addr="$1"
	shift
	ssh "${SSH_OPTS[@]}" "root@${addr}" "$@"
}

gateway_ssh() { remote_ssh "$TJ_TEST_REMOTE" "$@"; }

now_ns() { date +%s%N; }

# since prints the seconds from a nanosecond stamp until now, with one
# decimal, because the loss check reports how long a reconnect took.
since() {
	awk -v s="$1" -v e="$(date +%s%N)" 'BEGIN { printf "%.1f", (e - s) / 1e9 }'
}

# remote_helper_count prints the number of tj-helper processes on a remote.
# The anchor keeps the tailscaled ssh incubator, whose command line quotes
# the exec command, and the ssh command's own shell from matching.
remote_helper_count() {
	remote_ssh "$1" "pgrep -fc '^/[^ ]*/tj-helper\.'" 2>/dev/null || true
}

# remote_cache_listing prints the remote's tj cache directory. Empty output
# means clean.
remote_cache_listing() {
	remote_ssh "$1" 'ls -A "$HOME/.cache/tj" 2>/dev/null' || true
}

# remote_quic_listeners prints the UDP listeners of the remote in the QUIC
# port range, one per line, as "address:port".
remote_quic_listeners() {
	remote_ssh "$1" 'ss -Hlun' 2>/dev/null | awk '{print $4}' | grep -E ":${QUIC_PORT_RE}\$" || true
}

# remote_runtime_files prints the leftover files in /run/user/0 of a remote.
# Empty output means clean.
remote_runtime_files() {
	remote_ssh "$1" 'ls -A /run/user/0/tj-helper.* 2>/dev/null' || true
}

# wait_status polls tj status in the rig until the output matches the
# extended regular expression, and returns 1 when the deadline in seconds
# passes. The poll runs inside the rig, the method of reconnect.sh, so one
# status read costs no podman exec and a short transition still shows up.
wait_status() {
	podman exec -e "TJ_RE=$1" -e "TJ_DEADLINE=$2" "$CONTAINER" sh -c '
start=$(date +%s)
while :; do
	if tj status 2>/dev/null | grep -qE "$TJ_RE"; then exit 0; fi
	if [ $(($(date +%s) - start)) -ge "$TJ_DEADLINE" ]; then exit 1; fi
	sleep 0.2
done'
}

cleanup() {
	log "cleanup"
	podman rm -f "$CONTAINER" >/dev/null 2>&1 || true
}
trap cleanup EXIT

# remote_clean checks that a remote runs no helper process, keeps no cache
# file, keeps no /run/user/0 file, and has no listener in the QUIC range. It
# polls up to 10 s, because the helper of a lost transport exits when it
# sees the connection end, and that can take a moment.
remote_clean() {
	local label="$1" remote="$2" waited=0 helpers cache runtime listeners
	while :; do
		helpers="$(remote_helper_count "$remote")"
		cache="$(remote_cache_listing "$remote")"
		runtime="$(remote_runtime_files "$remote")"
		listeners="$(remote_quic_listeners "$remote")"
		if [ "${helpers:-0}" -eq 0 ] && [ -z "$cache" ] && [ -z "$runtime" ] && [ -z "$listeners" ]; then
			ok "${label}: the remote runs no helper, keeps no file, and has no udp listener in ${QUIC_PORTS}"
			return
		fi
		waited=$((waited + 1))
		if [ "$waited" -ge 10 ]; then
			bad "${label}: the remote is not clean: helpers=${helpers:-0} cache='${cache}' run/user/0='${runtime}' listeners='${listeners}'"
			return
		fi
		sleep 1
	done
}

# check_up checks 1: the RTT:, Path:, and Traffic: rows of tj status, and
# the metrics and path fields of tj status --json, within 5 s of the
# connect.
check_up() {
	local label="$1" deadline waited=0 json
	deadline=5
	while :; do
		json="$(rig tj status --json)"
		if printf '%s' "$json" | jq -e '
			(.metrics.rtt_ms // 0) > 0
			and .metrics.updated_at != null
			and (.path.latency_ms // 0) > 0
			and (.path.type == "direct" or .path.type == "relay")
		' >/dev/null 2>&1; then
			ok "${label}: status --json has rtt_ms, updated_at, and a path latency"
			break
		fi
		waited=$((waited + 1))
		if [ "$waited" -ge "$deadline" ]; then
			bad "${label}: status --json has no metrics and no path latency within 5 s: ${json}"
			break
		fi
		sleep 1
	done

	local human
	human="$(rig tj status)"
	printf '%s\n' "$human" | grep -E '^Path:|^RTT:|^Traffic:' || true

	if printf '%s\n' "$human" | grep -qE '^RTT:[[:space:]]+[0-9.]+ms$'; then
		ok "${label}: status has an RTT: row in ms"
	else
		bad "${label}: status has no RTT: row in ms"
	fi
	if printf '%s\n' "$human" | grep -qE '^Traffic:'; then
		ok "${label}: status has a Traffic: row"
	else
		bad "${label}: status has no Traffic: row"
	fi
	if printf '%s\n' "$human" | grep -qE '^Path:.*[0-9]ms$'; then
		ok "${label}: status Path: row ends in ms"
	else
		bad "${label}: status Path: row does not end in ms"
	fi
}

# check_counters is check 2: the traffic rates while a ping runs, and the
# byte totals after it.
check_counters() {
	local label="$1" before before_up before_down mid up_rate down_rate ping_pid after after_up after_down
	before="$(rig tj status --json)"
	before_up="$(printf '%s' "$before" | jq -r '.metrics.up_bytes // 0')"
	before_down="$(printf '%s' "$before" | jq -r '.metrics.down_bytes // 0')"

	rig ping -c 40 -i 0.2 -s 1200 "$GW_V4" >/dev/null 2>&1 &
	ping_pid=$!

	sleep 6
	mid="$(rig tj status --json)"
	up_rate="$(printf '%s' "$mid" | jq -r '.metrics.up_rate // 0')"
	down_rate="$(printf '%s' "$mid" | jq -r '.metrics.down_rate // 0')"
	if [ "$up_rate" -gt 0 ]; then
		ok "${label}: metrics.up_rate is above 0 during the ping (${up_rate})"
	else
		bad "${label}: metrics.up_rate is 0 during the ping"
	fi
	if [ "$down_rate" -gt 0 ]; then
		ok "${label}: metrics.down_rate is above 0 during the ping (${down_rate})"
	else
		bad "${label}: metrics.down_rate is 0 during the ping"
	fi

	wait "$ping_pid" || true
	sleep 6
	after="$(rig tj status --json)"
	after_up="$(printf '%s' "$after" | jq -r '.metrics.up_bytes // 0')"
	after_down="$(printf '%s' "$after" | jq -r '.metrics.down_bytes // 0')"
	if [ $((after_up - before_up)) -ge "$MIN_PING_BYTES" ]; then
		ok "${label}: up_bytes grew by $((after_up - before_up)), want at least ${MIN_PING_BYTES}"
	else
		bad "${label}: up_bytes grew by $((after_up - before_up)), want at least ${MIN_PING_BYTES}"
	fi
	if [ $((after_down - before_down)) -ge "$MIN_PING_BYTES" ]; then
		ok "${label}: down_bytes grew by $((after_down - before_down)), want at least ${MIN_PING_BYTES}"
	else
		bad "${label}: down_bytes grew by $((after_down - before_down)), want at least ${MIN_PING_BYTES}"
	fi
	rig tj status | grep -E '^Traffic:' || true
}

# check_interval is check 3: metrics.updated_at changes over the 5 s write
# interval.
check_interval() {
	local label="$1" first second
	first="$(rig tj status --json | jq -r '.metrics.updated_at')"
	sleep 7
	second="$(rig tj status --json | jq -r '.metrics.updated_at')"
	if [ "$first" != "$second" ]; then
		ok "${label}: metrics.updated_at changed after 7 s (${first} -> ${second})"
	else
		bad "${label}: metrics.updated_at did not change after 7 s (${first})"
	fi
}

# check_state_file is check 4: the state file mode and the absence of a
# leftover temp file, over 5 reads a second apart.
check_state_file() {
	local label="$1" mode leftover=0
	mode="$(rig stat -c '%a' /run/tj/session.json 2>/dev/null || true)"
	if [ "$mode" = "644" ]; then
		ok "${label}: session.json mode is 644"
	else
		bad "${label}: session.json mode is '${mode}', want 644"
	fi

	for _ in 1 2 3 4 5; do
		if rig test -e /run/tj/session.json.tmp; then
			leftover=1
		fi
		sleep 1
	done
	if [ "$leftover" -eq 0 ]; then
		ok "${label}: no session.json.tmp over 5 reads, 1 s apart"
	else
		bad "${label}: session.json.tmp was present during the 5 reads"
	fi
}

# check_atomic_reads is check 5: 50 back-to-back status --json reads all
# parse, which proves the atomic write leaves no partial document.
check_atomic_reads() {
	local label="$1" bad_reads=0
	for _ in $(seq 1 50); do
		if ! rig tj status --json | jq -e . >/dev/null 2>&1; then
			bad_reads=$((bad_reads + 1))
		fi
	done
	if [ "$bad_reads" -eq 0 ]; then
		ok "${label}: 50 status --json reads all parsed"
	else
		bad "${label}: ${bad_reads} of 50 status --json reads failed to parse"
	fi
}

# check_disconnect_clean is check 6: disconnect, then the runtime directory
# and the remote are clean.
check_disconnect_clean() {
	local label="$1" remote="$2"
	rig tj disconnect
	if rig test -e /run/tj/session.json || rig test -e /run/tj/session.json.tmp; then
		bad "${label}: the runtime directory still has a session file after disconnect"
	else
		ok "${label}: the runtime directory has no session file after disconnect"
	fi
	remote_clean "${label}: after disconnect" "$remote"
}

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

log "probe the gateway VPC IPv4 over Tailscale SSH"
GW_V4="$(gateway_ssh \
	"ip -4 -br addr show ens5 | awk '{print \$3}' | head -1 | cut -d/ -f1")"
printf 'gateway VPC IPv4: %s\n' "$GW_V4"

for transport in quic ssh; do
	label="transport ${transport}"
	log "${label}: tj connect ${TJ_TEST_REF} --dns none --transport ${transport}"
	rig tj -v connect "$TJ_TEST_REF" --user "$TJ_TEST_USER" --dns none --transport "$transport"

	check_up "$label"
	check_counters "$label"
	check_interval "$label"
	check_state_file "$label"
	check_atomic_reads "$label"
	check_disconnect_clean "$label" "$TJ_TEST_REMOTE"
done

log "loss (quic): connect with --reconnect-for 2m, then kill the helper on the remote"
rig tj -v connect "$TJ_TEST_REF" --user "$TJ_TEST_USER" --dns none --transport quic --reconnect-for 2m

loss_before_up="$(rig tj status --json | jq -r '.metrics.up_bytes // 0')"
kill_start="$(now_ns)"
gateway_ssh "pkill -f '^/[^ ]*/tj-helper\.'" || true

if wait_status '^Status:[[:space:]]+up' 60; then
	kill_to_up="$(since "$kill_start")"
	ok "loss (quic): the session reports up again ${kill_to_up} s after the kill"
	printf 'kill to up: %s s\n' "$kill_to_up"
else
	bad "loss (quic): the session did not report up again within 60 s"
	rig tj status || true
fi

reconnects_row="$(rig tj status | sed -n 's/^Reconnects:[[:space:]]*//p')"
if [ "$reconnects_row" = "1" ]; then
	ok "loss (quic): status shows 1 reconnect"
else
	bad "loss (quic): status Reconnects: row is '${reconnects_row}', want 1"
fi

loss_after_up="$(rig tj status --json | jq -r '.metrics.up_bytes // 0')"
if [ "$loss_after_up" -ge "$loss_before_up" ]; then
	ok "loss (quic): metrics.up_bytes did not drop (before ${loss_before_up}, after ${loss_after_up})"
else
	bad "loss (quic): metrics.up_bytes dropped (before ${loss_before_up}, after ${loss_after_up})"
fi

rtt_waited=0
rtt_back=0
while [ "$rtt_waited" -lt 10 ]; do
	if rig tj status | grep -qE '^RTT:'; then
		rtt_back=1
		break
	fi
	rtt_waited=$((rtt_waited + 1))
	sleep 1
done
if [ "$rtt_back" -eq 1 ]; then
	ok "loss (quic): the RTT: row is back within 10 s"
else
	bad "loss (quic): no RTT: row within 10 s of the session coming back up"
fi

check_disconnect_clean "loss (quic)" "$TJ_TEST_REMOTE"

if [ -n "${TJ_TEST_DERP_REF:-}" ]; then
	log "relayed remote: tj connect ${TJ_TEST_DERP_REF} --dns none on the default transport"
	rig tj -v connect "$TJ_TEST_DERP_REF" --user root --dns none
	check_up "relayed remote"
	rig tj status | grep -E '^Path:|^RTT:|^Traffic:' || true
	check_disconnect_clean "relayed remote" "$TJ_TEST_DERP_REMOTE"
fi

log "result: ${pass} passed, ${fail} failed"
[ "$fail" -eq 0 ]
