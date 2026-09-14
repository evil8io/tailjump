#!/usr/bin/env bash
# reconnect.sh proves that a session rebuilds its transport after a loss. It
# runs in the same rootless podman rig as run.sh, in its own container, and
# it runs every scenario once on the QUIC transport and once on the SSH
# transport.
#
# 1. A helper kill on the remote: the session reports reconnecting, it comes
#    back on the same unit with one reconnect, and DNS, TCP, and ICMP reach
#    the VPC again.
# 2. A drop of every packet from the remote for 30 s: the session reports
#    reconnecting, its DNS mode is reverted while the drop holds, and it
#    comes back after the drop.
# 3. A helper kill with the window off: the unit ends and the journal names
#    the end without a loss line.
# 4. A disconnect during a reconnect: it returns at once, the unit is gone,
#    and the remote keeps no helper process and no file.
# 5. A drop that outlives the window: the session gives up, and it leaves no
#    device, route, rule, or DNS change behind.
# 6. A gateway that leaves the tailnet: the session follows its tag to
#    another remote, or it ends because that remote serves another manifest.
#
# The drops are input rules. An output drop would fail the send at once with
# EPERM and skip the timeout path that a real loss takes.
#
# Scenario 6 takes a gateway off the tailnet for 90 s through a transient
# unit on that gateway, so the command outlives the SSH session that starts
# it. The script waits for the gateway to come back and fails loudly when it
# does not, because a gateway that stays offline needs its owner.
#
# It never runs tj connect on the host: the host holds its own session and
# the one-session rule forbids a second.
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
: "${TJ_TEST_RESOLVER:?set TJ_TEST_RESOLVER (the VPC resolver IPv4) in target.env or the environment}"
TJ_TEST_USER="${TJ_TEST_USER:-root}"
TJ_TEST_DNS_NAME="${TJ_TEST_DNS_NAME:-amazon.com}"

TSGW_SOCK="/var/run/tailscale/tailscaled.sock"
IMAGE="tj-e2e:latest"
CONTAINER="tj-e2e-reconnect-$$"
SSH_OPTS=(-o StrictHostKeyChecking=accept-new -o ConnectTimeout=15 -o BatchMode=yes)

# The deadlines of the checks, in seconds. A helper kill closes the mux at
# once, while a drop waits for a keepalive to time out, so the two losses
# get their own deadline.
KILL_DEADLINE=20
DROP_DEADLINE=25
UP_DEADLINE=45
END_DEADLINE=45
MOVE_DEADLINE=150
ONLINE_DEADLINE=180
REMOTE_CLEAN_DEADLINE=60

# The holds of the scenarios, in seconds.
DROP_HOLD=30
EXPIRY_HOLD=60
FLAP_SECONDS=90

pass=0
fail=0
skipped=0
dropped=0
flap_host=""
flap_addr=""
flap_start=0
SUMMARY=()

log() { printf '\n=== %s ===\n' "$*"; }
ok() { printf 'PASS: %s\n' "$*"; pass=$((pass + 1)); }
bad() { printf 'FAIL: %s\n' "$*"; fail=$((fail + 1)); }
skip() { printf 'SKIP: %s\n' "$*"; skipped=$((skipped + 1)); }

rig() { podman exec "$CONTAINER" "$@"; }

remote_ssh() {
	local addr="$1"
	shift
	ssh "${SSH_OPTS[@]}" "root@${addr}" "$@"
}

now_ns() { date +%s%N; }

# since prints the seconds from a nanosecond stamp until now.
since() {
	awk -v s="$1" -v e="$(date +%s%N)" 'BEGIN { printf "%.1f", (e - s) / 1e9 }'
}

# record adds one row to the table of measurements that the run prints at
# the end, because the numbers are the result of this test.
record() {
	SUMMARY+=("$(printf '| %s | %s | %s | %s |' "$1" "$2" "$3" "$4")")
}

# status_row prints the value of one tj status row, without its label.
status_row() {
	rig tj status 2>/dev/null | sed -n "s/^$1:[[:space:]]*//p"
}

# wait_status polls tj status in the rig until the output matches the
# extended regular expression, and returns 1 when the deadline in seconds
# passes. The poll runs inside the rig, so one status read costs no podman
# exec and a short transition still shows up.
wait_status() {
	podman exec -e "TJ_RE=$1" -e "TJ_DEADLINE=$2" "$CONTAINER" sh -c '
start=$(date +%s)
while :; do
	if tj status 2>/dev/null | grep -qE "$TJ_RE"; then exit 0; fi
	if [ $(($(date +%s) - start)) -ge "$TJ_DEADLINE" ]; then exit 1; fi
	sleep 0.2
done'
}

unit_active() { rig systemctl is-active tj-session >/dev/null 2>&1; }

unit_invocation() {
	rig systemctl show -p InvocationID --value tj-session 2>/dev/null | tr -d '\r' || true
}

# unit_journal prints the journal of one session unit run. The rig keeps
# every run of the unit, so a check reads the run it started itself.
unit_journal() {
	if [ -z "$1" ]; then
		return 0
	fi
	rig journalctl --no-pager "_SYSTEMD_INVOCATION_ID=$1" 2>/dev/null || true
}

# drop_remote drops every packet from the remote inside the rig, which is
# what a lost route or a gateway that left the tailnet does to a session.
drop_remote() {
	rig nft add table inet tjtest
	rig nft add chain inet tjtest input '{ type filter hook input priority 0; }'
	rig nft add rule inet tjtest input ip saddr "$1" drop
	dropped=1
}

undrop() {
	if [ "$dropped" -eq 1 ]; then
		rig nft delete table inet tjtest >/dev/null 2>&1 || true
		dropped=0
	fi
}

# hold_until sleeps until the given number of seconds passed since the
# nanosecond stamp, so a check that ran in between shortens no hold.
hold_until() {
	local rest
	rest="$(awk -v s="$1" -v e="$(date +%s%N)" -v w="$2" 'BEGIN { d = w - (e - s) / 1e9; if (d < 0) { d = 0 }; printf "%.1f", d }')"
	sleep "$rest"
}

tj0_present() {
	rig resolvectl status 2>/dev/null | grep -q '(tj0)'
}

# dns_on_device reports whether the session still routes DNS over its device.
# The device stays while the session reconnects, so this check reads the
# settings of the link, and not the list of links.
dns_on_device() {
	rig resolvectl status tj0 2>/dev/null | grep -q 'DNS Domain:'
}

# has_answer reports whether a dig answer holds an IPv4 address. It matches
# on captured output, because a match that closes the pipe early makes the
# command upstream look like it failed under pipefail.
has_answer() {
	printf '%s\n' "$1" | grep -qE '^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$'
}

# remote_helper_count prints the number of tj-helper processes on a remote.
# The anchor keeps the tailscaled ssh incubator, whose command line quotes
# the exec command, and the ssh command's own shell from matching.
remote_helper_count() {
	remote_ssh "$1" "pgrep -fc '^/[^ ]*/tj-helper\.'" 2>/dev/null || true
}

remote_cache_listing() {
	remote_ssh "$1" 'ls -A "$HOME/.cache/tj" 2>/dev/null' || true
}

remote_is_clean() {
	local helpers cache
	helpers="$(remote_helper_count "$1")"
	cache="$(remote_cache_listing "$1")"
	[ "${helpers:-0}" -eq 0 ] && [ -z "$cache" ]
}

# wait_remote_clean polls a remote until it runs no helper and keeps no
# file. A helper whose client is gone exits on its own, so the check waits.
wait_remote_clean() {
	local addr="$1" timeout="$2" waited=0
	while ! remote_is_clean "$addr"; do
		waited=$((waited + 5))
		if [ "$waited" -ge "$timeout" ]; then
			return 1
		fi
		sleep 5
	done
	return 0
}

# check_remotes waits until both gateways run no helper and keep no file. It
# waits, because the helper of a lost transport exits when its remote sees
# the connection end, and that can take longer than the check.
check_remotes() {
	local label="$1" addr start
	for addr in "$TJ_TEST_REMOTE" "${TJ_TEST_SECOND_REMOTE:-}"; do
		if [ -z "$addr" ]; then
			continue
		fi
		start="$(now_ns)"
		if wait_remote_clean "$addr" "$REMOTE_CLEAN_DEADLINE"; then
			ok "${label}: ${addr} runs no helper and keeps no file, after $(since "$start") s"
		else
			bad "${label}: ${addr} has $(remote_helper_count "$addr") helper processes and the cache entries '$(remote_cache_listing "$addr")'"
		fi
	done
}

# peer_online reports whether the host's tailscaled sees the peer online.
peer_online() {
	if command -v jq >/dev/null 2>&1; then
		tailscale status --json 2>/dev/null |
			jq -e --arg h "$1" 'any(.Peer[]; .HostName == $h and .Online)' >/dev/null 2>&1
		return
	fi
	tailscale status 2>/dev/null | awk -v h="$1" '$2 == h' | grep -qv offline
}

# gateway_back reports whether a gateway is back on the tailnet. The peer
# flag of tailscaled turns true before the gateway answers, so the check also
# opens SSH to it.
gateway_back() {
	peer_online "$1" && remote_ssh "$2" true >/dev/null 2>&1
}

wait_gateway_back() {
	local host="$1" addr="$2" timeout="$3" start
	start="$(now_ns)"
	while ! gateway_back "$host" "$addr"; do
		if awk -v s="$start" -v e="$(date +%s%N)" -v d="$timeout" 'BEGIN { exit !((e - s) / 1e9 >= d) }'; then
			return 1
		fi
		sleep 5
	done
	return 0
}

# tag_peer prints the hostname a tag resolves to, by the rule of tj: the
# online peer with that tag and the most recent handshake. It prints nothing
# without jq, and the caller then falls back to the check after the connect.
tag_peer() {
	if ! command -v jq >/dev/null 2>&1; then
		return 0
	fi
	tailscale status --json 2>/dev/null |
		jq -r --arg tag "$1" '[.Peer[] | select(.Online) | select((.Tags // []) | index($tag))] | sort_by(.LastHandshake) | last | .HostName // empty' 2>/dev/null || true
}

# wait_flap_unit waits until the flap unit of scenario 6 is gone from the
# gateway it ran on.
wait_flap_unit() {
	local waited=0
	while remote_ssh "$flap_addr" 'systemctl is-active tj-test-flap' >/dev/null 2>&1; do
		waited=$((waited + 5))
		if [ "$waited" -ge "$ONLINE_DEADLINE" ]; then
			printf 'WARNING: the flap unit on %s still runs\n' "$flap_host"
			return 1
		fi
		sleep 5
	done
	return 0
}

cleanup() {
	log "cleanup"
	undrop
	rig tj disconnect >/dev/null 2>&1 || true
	if [ -n "$flap_host" ]; then
		printf 'wait for %s to come back on the tailnet\n' "$flap_host"
		hold_until "$flap_start" "$FLAP_SECONDS"
		if wait_gateway_back "$flap_host" "$flap_addr" "$ONLINE_DEADLINE"; then
			wait_flap_unit || true
		else
			printf 'WARNING: %s is still offline; its owner must bring it back\n' "$flap_host"
		fi
	fi
	podman rm -f "$CONTAINER" >/dev/null 2>&1 || true
}
trap cleanup EXIT

# rig_connect connects in the rig. It fails the check when the connect fails,
# and also when it finds the session of an earlier scenario, because that
# session has the plan of that scenario.
rig_connect() {
	local out code=0
	out="$(rig tj -v connect "$@" 2>&1)" || code=$?
	printf '%s\n' "$out"
	if [ "$code" -ne 0 ]; then
		bad "connect failed: $*"
		rig_disconnect
		return 1
	fi
	case "$out" in
	*'already up'*)
		bad "connect found the session of an earlier scenario: $*"
		rig_disconnect
		return 1
		;;
	esac
	return 0
}

rig_disconnect() {
	rig tj disconnect >/dev/null 2>&1 || true
	sleep 2
}

# scenario1 kills the helper on the remote, which closes the transport at
# once, and checks that the session comes back on the same unit.
scenario1() {
	local t="$1"
	local label="scenario 1 (${t})"
	local invocation after start upstart reconnecting up row
	log "${label}: kill the helper on the remote"
	rig_connect "$TJ_TEST_REF" --user "$TJ_TEST_USER" --dns none --transport "$t" --reconnect-for 2m || return 0
	invocation="$(unit_invocation)"

	start="$(now_ns)"
	remote_ssh "$TJ_TEST_REMOTE" "pkill -f '^/[^ ]*/tj-helper\.'" || true
	if wait_status '^Status:[[:space:]]+reconnecting' "$KILL_DEADLINE"; then
		reconnecting="$(since "$start")"
		ok "${label}: status reports reconnecting ${reconnecting} s after the kill"
	else
		reconnecting="-"
		bad "${label}: status reports no reconnect within ${KILL_DEADLINE} s"
	fi
	upstart="$(now_ns)"
	if wait_status '^Status:[[:space:]]+up' "$UP_DEADLINE"; then
		up="$(since "$upstart")"
		ok "${label}: status reports up again ${up} s later"
	else
		up="-"
		bad "${label}: status reports no session up within ${UP_DEADLINE} s"
		rig tj status || true
	fi
	record "$t" "1 helper kill" "kill to reconnecting ${reconnecting} s" "reconnecting to up ${up} s"

	after="$(unit_invocation)"
	if [ -n "$invocation" ] && [ "$after" = "$invocation" ]; then
		ok "${label}: the session kept its unit"
	else
		bad "${label}: the unit changed from '${invocation}' to '${after}'"
	fi
	row="$(status_row Reconnects)"
	if [ "$row" = "1" ]; then
		ok "${label}: status counts 1 reconnect"
	else
		bad "${label}: status counts '${row}' reconnects, want 1"
	fi

	if has_answer "$(rig dig +time=5 +tries=2 +short "@${TJ_TEST_RESOLVER}" "$TJ_TEST_DNS_NAME" || true)"; then
		ok "${label}: UDP DNS to ${TJ_TEST_RESOLVER} works after the reconnect"
	else
		bad "${label}: UDP DNS to ${TJ_TEST_RESOLVER} fails after the reconnect"
	fi
	if rig nc -z -w 8 "$TJ_TEST_RESOLVER" 53; then
		ok "${label}: TCP to ${TJ_TEST_RESOLVER}:53 works after the reconnect"
	else
		bad "${label}: TCP to ${TJ_TEST_RESOLVER}:53 fails after the reconnect"
	fi
	local pings
	pings="$(rig ping -4 -n -c 3 -W 3 "$GW_V4" || true)"
	printf '%s\n' "$pings"
	case "$pings" in
	*'3 received'*) ok "${label}: ping to ${GW_V4} gets 3 of 3 replies after the reconnect" ;;
	*) bad "${label}: ping to ${GW_V4} loses replies after the reconnect" ;;
	esac
	rig_disconnect
}

# scenario2 drops every packet from the remote for 30 s, which is the loss a
# keepalive timeout reports, and checks the DNS revert and the recovery.
scenario2() {
	local t="$1"
	local label="scenario 2 (${t})"
	local start upstart reconnecting up
	log "${label}: drop every packet from the remote for ${DROP_HOLD} s"
	rig_connect "$TJ_TEST_REF" --user "$TJ_TEST_USER" --dns all --transport "$t" --reconnect-for 2m || return 0

	start="$(now_ns)"
	drop_remote "$TJ_TEST_REMOTE"
	if wait_status '^Status:[[:space:]]+reconnecting' "$DROP_DEADLINE"; then
		reconnecting="$(since "$start")"
		ok "${label}: status reports reconnecting ${reconnecting} s after the drop"
	else
		reconnecting="-"
		bad "${label}: status reports no reconnect within ${DROP_DEADLINE} s"
	fi

	if has_answer "$(rig dig +time=5 +tries=2 +short "$TJ_TEST_DNS_NAME" || true)"; then
		ok "${label}: ${TJ_TEST_DNS_NAME} resolves while the drop holds"
	else
		bad "${label}: ${TJ_TEST_DNS_NAME} does not resolve while the drop holds"
	fi
	if dns_on_device; then
		bad "${label}: the DNS configuration still routes over tj0 while the session reconnects"
	else
		ok "${label}: the DNS configuration is reverted while the session reconnects"
	fi

	hold_until "$start" "$DROP_HOLD"
	undrop
	upstart="$(now_ns)"
	if wait_status '^Status:[[:space:]]+up' "$UP_DEADLINE"; then
		up="$(since "$upstart")"
		ok "${label}: status reports up again ${up} s after the drop ended"
	else
		up="-"
		bad "${label}: status reports no session up within ${UP_DEADLINE} s after the drop ended"
		rig tj status || true
	fi
	record "$t" "2 network drop" "drop to reconnecting ${reconnecting} s" "drop end to up ${up} s"
	rig_disconnect
}

# scenario3 kills the helper with the window off, so the session ends
# instead of rebuilding its transport.
scenario3() {
	local t="$1"
	local label="scenario 3 (${t})"
	local invocation start ended journal
	log "${label}: kill the helper with the reconnect window off"
	rig_connect "$TJ_TEST_REF" --user "$TJ_TEST_USER" --dns none --transport "$t" --reconnect-for 0 || return 0
	invocation="$(unit_invocation)"

	start="$(now_ns)"
	remote_ssh "$TJ_TEST_REMOTE" "pkill -f '^/[^ ]*/tj-helper\.'" || true
	if wait_status '^no active session' "$END_DEADLINE"; then
		ended="$(since "$start")"
		ok "${label}: the session is gone ${ended} s after the kill"
	else
		ended="-"
		bad "${label}: the session still exists ${END_DEADLINE} s after the kill"
	fi
	record "$t" "3 reconnect off" "the unit ended" "kill to end ${ended} s"

	journal="$(unit_journal "$invocation")"
	if printf '%s\n' "$journal" | grep -q 'session ended: '; then
		ok "${label}: the journal names the end: $(printf '%s\n' "$journal" | grep -o 'session ended: .*' | head -1)"
	else
		bad "${label}: the journal has no session ended line"
		printf '%s\n' "$journal" | tail -n 20
	fi
	if printf '%s\n' "$journal" | grep -q 'session lost'; then
		bad "${label}: the journal has a session lost line with the window off"
	else
		ok "${label}: the journal has no session lost line"
	fi
	rig_disconnect
}

# scenario4 disconnects while the session reconnects. The disconnect must
# not wait for the attempt that runs, and the remote must end up clean.
scenario4() {
	local t="$1"
	local label="scenario 4 (${t})"
	local start elapsed
	log "${label}: disconnect while the session reconnects"
	rig_connect "$TJ_TEST_REF" --user "$TJ_TEST_USER" --dns none --transport "$t" --reconnect-for 2m || return 0

	drop_remote "$TJ_TEST_REMOTE"
	if ! wait_status '^Status:[[:space:]]+reconnecting' "$DROP_DEADLINE"; then
		bad "${label}: status reports no reconnect within ${DROP_DEADLINE} s"
		undrop
		rig_disconnect
		return 0
	fi

	start="$(now_ns)"
	rig tj disconnect || bad "${label}: the disconnect failed"
	elapsed="$(since "$start")"
	if awk -v e="$elapsed" 'BEGIN { exit !(e <= 2.0) }'; then
		ok "${label}: the disconnect returned after ${elapsed} s"
	else
		bad "${label}: the disconnect took ${elapsed} s, want at most 2 s"
	fi
	if unit_active; then
		bad "${label}: the session unit is still active right after the disconnect"
	else
		ok "${label}: the session unit is gone right after the disconnect"
	fi
	record "$t" "4 disconnect during a reconnect" "the unit is gone" "disconnect ${elapsed} s"
	undrop

	if wait_remote_clean "$TJ_TEST_REMOTE" "$REMOTE_CLEAN_DEADLINE"; then
		ok "${label}: ${TJ_TEST_REMOTE} runs no helper and keeps no file"
	else
		bad "${label}: ${TJ_TEST_REMOTE} has $(remote_helper_count "$TJ_TEST_REMOTE") helper processes and the cache entries '$(remote_cache_listing "$TJ_TEST_REMOTE")'"
	fi
}

# scenario5 holds the drop past the reconnect window, so the session gives
# up. It then checks that the session left nothing behind.
scenario5() {
	local t="$1"
	local label="scenario 5 (${t})"
	local invocation start ended journal
	log "${label}: hold a drop past a 20 s reconnect window"
	rig_connect "$TJ_TEST_REF" --user "$TJ_TEST_USER" --dns none --transport "$t" --reconnect-for 20s || return 0
	invocation="$(unit_invocation)"

	start="$(now_ns)"
	drop_remote "$TJ_TEST_REMOTE"
	if wait_status '^no active session' "$EXPIRY_HOLD"; then
		ended="$(since "$start")"
		ok "${label}: the session gave up ${ended} s after the drop"
	else
		ended="-"
		bad "${label}: the session still exists ${EXPIRY_HOLD} s after the drop"
	fi
	hold_until "$start" "$EXPIRY_HOLD"
	undrop
	record "$t" "5 window expiry" "the unit ended" "drop to give up ${ended} s"

	journal="$(unit_journal "$invocation")"
	if printf '%s\n' "$journal" | grep -q 'reconnect gave up after'; then
		ok "${label}: the journal names the give-up: $(printf '%s\n' "$journal" | grep -o 'reconnect gave up after.*' | head -1)"
	else
		bad "${label}: the journal has no give-up line"
		printf '%s\n' "$journal" | tail -n 20
	fi
	if unit_active; then
		bad "${label}: the session unit is still active"
	else
		ok "${label}: the session unit is gone"
	fi
	if rig ip link show tj0 >/dev/null 2>&1; then
		bad "${label}: the tj0 device still exists"
	else
		ok "${label}: the tj0 device is gone"
	fi
	if [ -z "$(rig ip route show table 117 2>/dev/null)$(rig ip -6 route show table 117 2>/dev/null)" ]; then
		ok "${label}: the session table is empty"
	else
		bad "${label}: the session table still has routes"
	fi
	if rig ip rule show | grep -q '^5300:' || rig ip -6 rule show | grep -q '^5300:'; then
		bad "${label}: a session rule remains"
	else
		ok "${label}: no session rule remains"
	fi
	if tj0_present; then
		bad "${label}: the DNS configuration still names tj0"
	else
		ok "${label}: the DNS configuration names no tj0 link"
	fi
	rig_disconnect
}

# scenario6 takes the gateway of the session off the tailnet, so the tag
# resolves to another remote. The session then moves, or it ends because
# the other remote serves another manifest. Both outcomes are correct.
scenario6() {
	local t="$1"
	local label="scenario 6 (${t})"
	local predicted row host addr start outcome elapsed journal newrow invocation
	log "${label}: take the gateway of the session off the tailnet"
	if [ -z "${TJ_TEST_TAG:-}" ] || [ -z "${TJ_TEST_SECOND_REF:-}" ]; then
		skip "${label}: TJ_TEST_TAG or TJ_TEST_SECOND_REF is unset"
		return 0
	fi

	# The tag resolves to the online peer with the most recent handshake,
	# which can be a gateway this test may not touch. The check runs before
	# the connect, so no such gateway gets a helper.
	predicted="$(tag_peer "$TJ_TEST_TAG")"
	if [ -n "$predicted" ] && [ "$predicted" != "$TJ_TEST_REF" ] && [ "$predicted" != "$TJ_TEST_SECOND_REF" ]; then
		skip "${label}: ${TJ_TEST_TAG} resolves to ${predicted}, which is no gateway of this test"
		return 0
	fi

	rig_connect "$TJ_TEST_TAG" --user "$TJ_TEST_USER" --dns none --transport "$t" --reconnect-for 3m || return 0
	invocation="$(unit_invocation)"
	row="$(status_row Remote)"
	host="${row%% *}"
	addr="$(printf '%s' "$row" | sed -E 's/.*\(([^)]*)\).*/\1/')"
	if [ "$host" != "$TJ_TEST_REF" ] && [ "$host" != "$TJ_TEST_SECOND_REF" ]; then
		skip "${label}: ${TJ_TEST_TAG} resolved to ${host}, which is no gateway of this test"
		rig_disconnect
		return 0
	fi
	printf '%s resolved to %s\n' "$TJ_TEST_TAG" "$row"

	flap_host="$host"
	flap_addr="$addr"
	# tailscale down closes this SSH session, so ssh reports a failure that
	# says nothing about the transient unit it started.
	remote_ssh "$addr" "systemd-run --unit tj-test-flap --collect /bin/sh -c 'tailscale down; sleep ${FLAP_SECONDS}; tailscale up'" || true
	start="$(now_ns)"
	flap_start="$start"

	if ! wait_status '^Status:[[:space:]]+reconnecting' "$DROP_DEADLINE"; then
		bad "${label}: status reports no reconnect within ${DROP_DEADLINE} s"
	fi

	outcome=""
	while :; do
		if ! unit_active; then
			outcome=ended
			break
		fi
		if rig tj status 2>/dev/null | grep -qE '^Status:[[:space:]]+up'; then
			outcome=moved
			break
		fi
		if awk -v s="$start" -v e="$(date +%s%N)" -v d="$MOVE_DEADLINE" 'BEGIN { exit !((e - s) / 1e9 >= d) }'; then
			break
		fi
		sleep 1
	done
	elapsed="$(since "$start")"
	journal="$(unit_journal "$invocation")"

	case "$outcome" in
	moved)
		newrow="$(status_row Remote)"
		if [ "$newrow" != "$row" ]; then
			ok "${label}: the session moved from ${row} to ${newrow} in ${elapsed} s"
		else
			bad "${label}: the session reports up on ${newrow}, which is the gateway that left"
		fi
		record "$t" "6 move" "up on ${newrow%% *}" "move ${elapsed} s"
		rig_disconnect
		;;
	ended)
		if printf '%s\n' "$journal" | grep -q 'remote changed'; then
			ok "${label}: the session ended on another manifest in ${elapsed} s: $(printf '%s\n' "$journal" | grep -o 'remote changed.*' | head -1)"
		else
			bad "${label}: the session ended without a remote changed line"
			printf '%s\n' "$journal" | tail -n 20
		fi
		record "$t" "6 move" "ended on another manifest" "end ${elapsed} s"
		;;
	*)
		bad "${label}: neither a move nor an end within ${MOVE_DEADLINE} s"
		printf '%s\n' "$journal" | tail -n 20
		record "$t" "6 move" "no outcome" "${elapsed} s"
		rig_disconnect
		;;
	esac
	if printf '%s\n' "$journal" | grep -q 'remote moved from'; then
		printf 'journal: %s\n' "$(printf '%s\n' "$journal" | grep -o 'remote moved from.*' | head -1)"
	fi

	log "${label}: wait for ${flap_host} to come back on the tailnet"
	hold_until "$flap_start" "$FLAP_SECONDS"
	if wait_gateway_back "$flap_host" "$flap_addr" "$ONLINE_DEADLINE"; then
		ok "${label}: ${flap_host} is on the tailnet again"
	else
		bad "${label}: ${flap_host} is still offline after ${ONLINE_DEADLINE} s"
		printf '\n!!! %s did not come back on the tailnet. Its owner must bring it back. !!!\n\n' "$flap_host"
		exit 1
	fi
	wait_flap_unit || true
	flap_host=""
	flap_addr=""
	# The gateway needs a moment to hold a path again before the next
	# scenario dials it.
	sleep 10
}

log "build tj (host arch) with the embedded helpers"
(cd "$ROOT" && task build)

log "probe the gateway VPC IPv4 over Tailscale SSH"
GW_V4="$(remote_ssh "$TJ_TEST_REMOTE" \
	"ip -4 -br addr show ens5 | awk '{print \$3}' | head -1 | cut -d/ -f1")"
printf 'gateway VPC IPv4: %s\n' "$GW_V4"

log "build the rig image"
podman build -t "$IMAGE" -f "$DIR/Containerfile" "$DIR"

log "start the rig"
# --dns-search=. drops the search domains of the host, so a tj session on the
# host does not give the rig's default link a domain of the test.
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

log "confirm the gateways are clean before the run"
check_remotes "before the run"

for transport in quic ssh; do
	log "transport ${transport}"
	scenario1 "$transport"
	scenario2 "$transport"
	scenario3 "$transport"
	scenario4 "$transport"
	scenario5 "$transport"
	scenario6 "$transport"
done

log "confirm the gateways are clean after the run"
check_remotes "after the run"

log "measurements"
printf '| Transport | Scenario | Result | Seconds |\n'
printf '| -- | -- | -- | -- |\n'
for row in "${SUMMARY[@]}"; do
	printf '%s\n' "$row"
done

log "result: ${pass} passed, ${fail} failed, ${skipped} skipped"
[ "$fail" -eq 0 ]
