#!/usr/bin/env bash
# compare.sh runs the tj against sshuttle comparison of spike 9 on this
# machine, one tunnel at a time, and prints the measurements. The rig cannot
# run the real direct path, so this job runs on the client on purpose. It
# reads the remote from test/e2e/target.env, or from the file in TJ_TEST_ENV,
# so the relayed remote is a second env file.
#
# Sessions: quic (tj, --dns all), ssh (tj --transport ssh), sshuttle (the
# flags of the Evil8 connect task plus --dns), sshuttle-buf (the same with
# --latency-buffer-size TJ_TEST_SSHUTTLE_BUF). Per session: a time-boxed
# download from the bench server on the remote's VPC address with probes every
# 5 s (a DNS query, a TCP connect, a disco ping), 20 TCP connects, 10 DNS
# queries, a system-resolver lookup of TJ_TEST_PRIVATE_NAME, and on a
# dual-stack remote a download over IPv6. The DNS queries use TCP through
# sshuttle, because sshuttle captures no UDP to the VPC resolver. The TCP
# connect number is not comparable through sshuttle, which accepts the
# connection on this machine.
#
# sshuttle needs a NOPASSWD sudo rule for its firewall helper, because the job
# has no terminal. Start one short sshuttle session by hand first.
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
ENV_FILE="${TJ_TEST_ENV:-${ROOT}/test/e2e/target.env}"
if [ ! -f "$ENV_FILE" ]; then
	echo "compare: ${ENV_FILE} is missing" >&2
	exit 2
fi
# shellcheck disable=SC1090
. "$ENV_FILE"

REF="${TJ_TEST_REF:?}"
REMOTE="${TJ_TEST_REMOTE:?}"
USER_="${TJ_TEST_USER:-root}"
V4="${TJ_TEST_VPC_V4:?}"
V6="${TJ_TEST_VPC_V6:-}"
V6PREFIX="${TJ_TEST_VPC_V6_PREFIX:-}"
RESOLVER="${TJ_TEST_RESOLVER:?}"
DNS_NAME="${TJ_TEST_DNS_NAME:-amazon.com}"
PRIVATE_NAME="${TJ_TEST_PRIVATE_NAME:-}"
BENCH_PORT="${TJ_TEST_BENCH_PORT:-7460}"
SESSIONS="${TJ_TEST_SESSIONS:-quic sshuttle ssh}"
CYCLES="${TJ_TEST_CYCLES:-2}"
CYCLE_START="${TJ_TEST_CYCLE_START:-1}"
DL_SECONDS="${TJ_TEST_DL_SECONDS:-60}"
SSHUTTLE_NETS="${TJ_TEST_SSHUTTLE_NETS:-10.0.0.0/8}"
SSHUTTLE_BUF="${TJ_TEST_SSHUTTLE_BUF:-4194304}"
TJ="${TJ_TEST_TJ:-tj}"
LOCK="${TJ_TEST_LOCK:-/tmp/tj-e2e.lock}"
PROBE_EVERY=5
SERVER_SECONDS=1500
PIDFILE="${XDG_RUNTIME_DIR:-/tmp}/sshuttle-compare-${USER}.pid"
SSH_OPTS=(-o StrictHostKeyChecking=accept-new -o ConnectTimeout=15 -o BatchMode=yes)
BENCH="${ROOT}/bin/bench"
TMP="$(mktemp -d)"

log() { printf '%s %s\n' "$(date +%H:%M:%S)" "$*"; }
remote_ssh() { ssh "${SSH_OPTS[@]}" "${USER_}@${REMOTE}" "$@"; }
sshuttle_pids() { pgrep -f "sshuttle (-v|--method)" || true; }

cleanup() {
	log "cleanup"
	"$TJ" status 2>/dev/null | grep -q 'no active session' || "$TJ" disconnect
	if [ -f "$PIDFILE" ]; then
		kill "$(cat "$PIDFILE")" 2>/dev/null
		sleep 2
		rm -f "$PIDFILE"
	fi
	remote_ssh 'pkill -f "tj-benc[h]"; rmdir "$HOME/.cache/tj" 2>/dev/null; true' >/dev/null 2>&1 || true
	left="$(remote_ssh 'ls -A "$HOME/.cache/tj" 2>/dev/null; ss -Hltn | grep ":'"${BENCH_PORT}"' "; pgrep -af "tj-benc[h]"' || true)"
	if [ -z "$left" ]; then
		log "remote clean: no bench file, listener, or process remains"
	else
		log "remote residue: ${left}"
	fi
	log "final tj status: $("$TJ" status 2>&1 | tr '\n' ' ')"
	log "final ip rule:"
	ip rule | sed 's/^/    /'
	log "final sshuttle: $(sshuttle_pids | tr '\n' ' ')"
	rm -rf "$TMP"
}
trap cleanup EXIT

exec 9>"$LOCK"
if ! flock -w 3600 9; then
	log "lock busy: ${LOCK}"
	exit 1
fi

log "compare ${REF} remote=${REMOTE} v4=${V4} v6=${V6:-none} cycles=${CYCLE_START}..$((CYCLE_START + CYCLES - 1)) sessions=${SESSIONS}"
log "tj $("$TJ" version) sshuttle $(sshuttle --version)"
"$TJ" status | grep -q 'no active session' || { log "precondition: a tj session is active"; exit 1; }
[ -n "$(sshuttle_pids)" ] && { log "precondition: an sshuttle process runs"; exit 1; }
ip rule | grep -q 'lookup 117' && { log "precondition: a rule for table 117 exists"; exit 1; }
log "path: $(tailscale ping -c 1 --timeout 3s "$REMOTE" 2>&1 | tail -1)"

REMOTE_ARCH="$(remote_ssh 'uname -m')"
case "$REMOTE_ARCH" in
x86_64) REMOTE_GOARCH=amd64 ;;
aarch64) REMOTE_GOARCH=arm64 ;;
*) log "unsupported remote arch ${REMOTE_ARCH}"; exit 1 ;;
esac
(cd "$ROOT" && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o bin/bench ./test/e2e/loss/bench)
(cd "$ROOT" && CGO_ENABLED=0 GOOS=linux GOARCH="$REMOTE_GOARCH" go build -trimpath -ldflags "-s -w" -o "bin/bench-linux-${REMOTE_GOARCH}" ./test/e2e/loss/bench)

LISTEN="${V4}:${BENCH_PORT}"
[ -n "$V6" ] && LISTEN="${LISTEN},[${V6}]:${BENCH_PORT}"
remote_ssh 'pkill -f "tj-benc[h]"; true' >/dev/null 2>&1 || true
SERVER_PATH="$(remote_ssh 'd="$HOME/.cache/tj"; mkdir -p "$d" && f="$d/tj-bench.$$" && cat > "$f" && chmod 0700 "$f" && echo "$f"' < "${ROOT}/bin/bench-linux-${REMOTE_GOARCH}")"
remote_ssh "setsid nohup timeout ${SERVER_SECONDS} ${SERVER_PATH} serve -listen ${LISTEN} -seconds ${SERVER_SECONDS} >/dev/null 2>&1 < /dev/null &"
sleep 2
if remote_ssh "ss -Hltn | grep ':${BENCH_PORT} '" >/dev/null; then
	log "bench server listens on ${LISTEN}"
else
	log "the bench server did not start"
	exit 1
fi

connect_session() {
	case "$1" in
	quic) "$TJ" connect "$REF" --user "$USER_" --dns all --transport quic ;;
	ssh) "$TJ" connect "$REF" --user "$USER_" --dns all --transport ssh ;;
	sshuttle | sshuttle-buf)
		local extra=() nets
		[ "$1" = sshuttle-buf ] && extra=(--latency-buffer-size "$SSHUTTLE_BUF")
		read -r -a nets <<<"$SSHUTTLE_NETS"
		[ -n "$V6PREFIX" ] && nets+=("$V6PREFIX")
		sshuttle -v --daemon --pidfile "$PIDFILE" --remote "${USER_}@${REMOTE}" \
			--ssh-cmd 'ssh -oStrictHostKeyChecking=accept-new -oServerAliveInterval=60' \
			"${extra[@]}" --dns "${nets[@]}"
		;;
	*) log "unknown session ${1}"; return 1 ;;
	esac
}

disconnect_session() {
	case "$1" in
	quic | ssh) "$TJ" disconnect ;;
	sshuttle | sshuttle-buf)
		[ -f "$PIDFILE" ] && kill "$(cat "$PIDFILE")" 2>/dev/null
		for _ in $(seq 1 30); do
			[ -z "$(sshuttle_pids)" ] && break
			sleep 0.5
		done
		rm -f "$PIDFILE"
		;;
	esac
}

verify_clean() {
	log "  tj status: $("$TJ" status 2>&1 | tr '\n' ' ')"
	log "  rule 117: $(ip rule | grep 117 || echo none)"
	log "  sshuttle: $(sshuttle_pids | tr '\n' ' ') pidfile: $([ -f "$PIDFILE" ] && echo present || echo absent)"
	log "  resolver unreachable check: $("$BENCH" connect -addr "${RESOLVER}:53" -n 1 -timeout 3s 2>/dev/null)"
}

wait_ready() {
	local out
	for _ in $(seq 1 30); do
		out="$("$BENCH" connect -addr "${RESOLVER}:53" -n 1 -timeout 3s 2>/dev/null)"
		case "$out" in *"failed=0"*) return 0 ;; esac
		sleep 1
	done
	return 1
}

dig_ms() { dig +time=2 +tries=1 "$@" 2>&1 | awk '/Query time/{print $4} /timed out|no servers/{print "FAIL"}'; }

measure() {
	local s="$1" cyc="$2" digopt="" gpid t0 el d c p times="" fails=0 q a
	case "$s" in sshuttle*) digopt="+tcp" ;; esac
	case "$s" in
	sshuttle*) log "[$cyc/$s] sshuttle pid $(cat "$PIDFILE" 2>/dev/null) path: $(tailscale ping -c 1 --timeout 3s "$REMOTE" 2>&1 | tail -1)" ;;
	*) log "[$cyc/$s] $("$TJ" status 2>&1 | tr '\n' ' ')" ;;
	esac
	"$BENCH" get -addr "${V4}:${BENCH_PORT}" -mib 65536 -timeout "${DL_SECONDS}s" >"$TMP/get.out" 2>&1 &
	gpid=$!
	t0=$(date +%s)
	while kill -0 "$gpid" 2>/dev/null; do
		el=$(($(date +%s) - t0))
		# shellcheck disable=SC2086
		d="$(dig_ms $digopt @"$RESOLVER" "$DNS_NAME")"
		c="$("$BENCH" connect -addr "${RESOLVER}:53" -n 1 -timeout 3s 2>/dev/null | grep -o 'failed=[0-9]* p50_ms=[0-9.]*')"
		p="$(tailscale ping -c 1 --timeout 3s "$REMOTE" 2>&1 | grep -o 'via [^ ]*\|timed out\|direct connection not established')"
		log "[$cyc/$s] probe t=${el}s dns=${d:-FAIL}ms ${c} ping=${p:-timeout}"
		sleep "$PROBE_EVERY"
	done
	wait "$gpid"
	log "[$cyc/$s] download v4 ${DL_SECONDS}s: $(cat "$TMP/get.out")"
	log "[$cyc/$s] connect x20: $("$BENCH" connect -addr "${RESOLVER}:53" -n 20 2>/dev/null)"
	for _ in $(seq 1 10); do
		# shellcheck disable=SC2086
		q="$(dig_ms $digopt @"$RESOLVER" "$DNS_NAME")"
		case "$q" in "" | FAIL) fails=$((fails + 1)) ;; *) times="${times} ${q}" ;; esac
	done
	log "[$cyc/$s] dns x10 ms:${times} failed=${fails}"
	if [ -n "$PRIVATE_NAME" ]; then
		a="$(dig +time=2 +tries=1 +short "$PRIVATE_NAME" 2>&1 | head -1)"
		log "[$cyc/$s] system resolver ${PRIVATE_NAME} -> ${a:-no answer}"
	fi
	if [ -n "$V6" ]; then
		log "[$cyc/$s] download v6 30s: $("$BENCH" get -addr "[${V6}]:${BENCH_PORT}" -mib 65536 -timeout 30s 2>&1)"
	fi
}

for cyc in $(seq "$CYCLE_START" $((CYCLE_START + CYCLES - 1))); do
	for s in $SESSIONS; do
		log "=== cycle ${cyc} session ${s} connect"
		connect_session "$s" >"$TMP/connect.out" 2>&1
		rc=$?
		sed 's/^/    /' "$TMP/connect.out"
		[ $rc -ne 0 ] && log "[$cyc/$s] connect exit ${rc}"
		if ! wait_ready; then
			log "[$cyc/$s] NOT READY, session skipped"
			disconnect_session "$s"
			verify_clean
			continue
		fi
		measure "$s" "$cyc"
		log "=== cycle ${cyc} session ${s} disconnect"
		disconnect_session "$s" 2>&1 | sed 's/^/    /'
		verify_clean
	done
done
log "compare ${REF} done"
