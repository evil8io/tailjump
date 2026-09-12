#!/usr/bin/env bash
# loss.sh measures a tj session under packet loss and delay, inside the
# rootless podman rig. It builds tj and the bench tool, starts the bench
# server on the remote's VPC address, starts the rig, and for each transport
# connects, applies tc netem loss and delay on the rig's own interface in
# both directions, measures TCP connect latency, UDP DNS success, and a bulk
# download through the session, removes the shaping, and disconnects. It
# prints one table per transport and one RESULT line per transport.
#
# The shaping goes on after the session is up, so the numbers describe the
# data plane under loss and not the SSH bootstrap. It never runs tj connect
# on the host and never shapes a host interface: the shaping lives in the
# container's network namespace.
#
# Knobs, all optional: TJ_TEST_TARGET gateway|derp (default gateway),
# TJ_TEST_LOSS_PCT (default 7, 0 for none), TJ_TEST_DELAY_MS (default 30),
# TJ_TEST_TRANSPORTS (default "quic ssh"), TJ_TEST_MEASURES (default
# "connect dns bulk"), TJ_TEST_BULK_MIB (default 256), TJ_TEST_BULK_RUNS
# (default 3), TJ_TEST_BULK_TIMEOUT seconds (default 60), TJ_TEST_IPV6
# (default 1), TJ_TEST_BENCH_PORT (default 7460), TJ_TEST_LABEL (a note for
# the table), and TJ_QUIC_CONTROLLER (the measurement knob, passed to the
# client in the rig).
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

TARGET="${TJ_TEST_TARGET:-gateway}"
case "$TARGET" in
gateway)
	REF="${TJ_TEST_REF:?set TJ_TEST_REF in target.env or the environment}"
	REMOTE="${TJ_TEST_REMOTE:?set TJ_TEST_REMOTE in target.env or the environment}"
	RESOLVER="${TJ_TEST_RESOLVER:?set TJ_TEST_RESOLVER in target.env or the environment}"
	;;
derp)
	REF="${TJ_TEST_DERP_REF:?set TJ_TEST_DERP_REF for the relayed remote}"
	REMOTE="${TJ_TEST_DERP_REMOTE:?set TJ_TEST_DERP_REMOTE for the relayed remote}"
	RESOLVER="${TJ_TEST_DERP_RESOLVER:?set TJ_TEST_DERP_RESOLVER for the relayed remote}"
	;;
*)
	printf 'unknown TJ_TEST_TARGET %s, want gateway or derp\n' "$TARGET"
	exit 2
	;;
esac
USER_="${TJ_TEST_USER:-root}"
DNS_NAME="${TJ_TEST_DNS_NAME:-amazon.com}"
LOSS="${TJ_TEST_LOSS_PCT:-7}"
DELAY="${TJ_TEST_DELAY_MS:-30}"
TRANSPORTS="${TJ_TEST_TRANSPORTS:-quic ssh}"
MEASURES="${TJ_TEST_MEASURES:-connect dns bulk}"
BULK_MIB="${TJ_TEST_BULK_MIB:-256}"
BULK_RUNS="${TJ_TEST_BULK_RUNS:-3}"
BULK_TIMEOUT="${TJ_TEST_BULK_TIMEOUT:-60}"
IPV6="${TJ_TEST_IPV6:-1}"
BENCH_PORT="${TJ_TEST_BENCH_PORT:-7460}"
LABEL="${TJ_TEST_LABEL:-}"
CONNECT_N=20
DNS_N=20
SERVER_SECONDS=2400

TSGW_SOCK="/var/run/tailscale/tailscaled.sock"
IMAGE="tj-e2e:latest"
CONTAINER="tj-loss-$$"
SSH_OPTS=(-o StrictHostKeyChecking=accept-new -o ConnectTimeout=15 -o BatchMode=yes)

log() { printf '\n=== %s ===\n' "$*"; }
rig() { podman exec "$CONTAINER" "$@"; }
remote_ssh() { ssh "${SSH_OPTS[@]}" "root@${REMOTE}" "$@"; }

# rig_connect runs tj connect in the rig and passes the controller knob
# through when it is set.
rig_connect() {
	if [ -n "${TJ_QUIC_CONTROLLER:-}" ]; then
		podman exec -e "TJ_QUIC_CONTROLLER=${TJ_QUIC_CONTROLLER}" "$CONTAINER" tj -v connect "$@"
	else
		rig tj -v connect "$@"
	fi
}

# rig_iface prints the rig's outward interface, the first link that is not
# lo. pasta names it after the host interface, so it is never hardcoded.
rig_iface() {
	rig sh -c "ip -br link | awk '\$1 != \"lo\" {print \$1; exit}' | cut -d@ -f1"
}

shaped=0
shape_on() {
	if [ "$LOSS" = "0" ] && [ "$DELAY" = "0" ]; then
		return 0
	fi
	local iface
	iface="$(rig_iface)"
	rig tc qdisc add dev "$iface" root netem delay "${DELAY}ms" loss "${LOSS}%"
	rig ip link add ifb0 type ifb
	rig ip link set ifb0 up
	rig tc qdisc add dev "$iface" handle ffff: ingress
	rig tc filter add dev "$iface" parent ffff: matchall action mirred egress redirect dev ifb0
	rig tc qdisc add dev ifb0 root netem delay "${DELAY}ms" loss "${LOSS}%"
	shaped=1
	printf 'shaping on %s: delay %s ms and loss %s%% in each direction\n' "$iface" "$DELAY" "$LOSS"
}

shape_off() {
	if [ "$shaped" -eq 0 ]; then
		return 0
	fi
	local iface
	iface="$(rig_iface)"
	rig tc qdisc del dev "$iface" root >/dev/null 2>&1 || true
	rig tc qdisc del dev "$iface" ingress >/dev/null 2>&1 || true
	rig ip link del ifb0 >/dev/null 2>&1 || true
	shaped=0
}

server_started=0
cleanup() {
	log "cleanup"
	shape_off || true
	rig tj disconnect >/dev/null 2>&1 || true
	if [ "$server_started" -eq 1 ]; then
		remote_ssh 'pkill -f tj-bench; rmdir "$HOME/.cache/tj" 2>/dev/null; true' >/dev/null 2>&1 || true
	fi
	podman rm -f "$CONTAINER" >/dev/null 2>&1 || true
}
trap cleanup EXIT

# median prints the median of the numbers on stdin, one per line.
median() {
	sort -n | awk '{ a[NR] = $1 } END { if (NR == 0) { print "0" } else if (NR % 2) { print a[(NR + 1) / 2] } else { printf "%.1f\n", (a[NR / 2] + a[NR / 2 + 1]) / 2 } }'
}

log "build tj and the bench tool"
(cd "$ROOT" && task build)
REMOTE_ARCH="$(remote_ssh 'uname -m')"
case "$REMOTE_ARCH" in
x86_64) REMOTE_GOARCH=amd64 ;;
aarch64) REMOTE_GOARCH=arm64 ;;
*)
	printf 'unsupported remote architecture %s\n' "$REMOTE_ARCH"
	exit 1
	;;
esac
(cd "$ROOT" && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o bin/bench ./test/e2e/loss/bench)
(cd "$ROOT" && CGO_ENABLED=0 GOOS=linux GOARCH="$REMOTE_GOARCH" go build -trimpath -ldflags "-s -w" -o "bin/bench-linux-${REMOTE_GOARCH}" ./test/e2e/loss/bench)

log "probe the remote's VPC addresses"
REMOTE_V4="$(remote_ssh "ip -4 route get ${RESOLVER} | awk '{for (i = 1; i <= NF; i++) if (\$i == \"src\") print \$(i + 1)}' | head -1")"
if [ -z "$REMOTE_V4" ]; then
	printf 'could not find the remote VPC IPv4 address\n'
	exit 1
fi
REMOTE_V6=""
if [ "$IPV6" = "1" ]; then
	REMOTE_V6="$(remote_ssh "ip -6 -br addr show scope global | awk '{print \$3}' | head -1 | cut -d/ -f1" || true)"
fi
printf 'remote VPC IPv4: %s\nremote VPC IPv6: %s\n' "$REMOTE_V4" "${REMOTE_V6:-none}"
LISTEN="${REMOTE_V4}:${BENCH_PORT}"
if [ -n "$REMOTE_V6" ]; then
	LISTEN="${LISTEN},[${REMOTE_V6}]:${BENCH_PORT}"
fi

log "start the bench server on the remote"
SERVER_PATH="$(remote_ssh 'd="$HOME/.cache/tj"; mkdir -p "$d" && f="$d/tj-bench.$$" && cat > "$f" && chmod 0700 "$f" && echo "$f"' < "${ROOT}/bin/bench-linux-${REMOTE_GOARCH}")"
remote_ssh "setsid nohup timeout ${SERVER_SECONDS} ${SERVER_PATH} serve -listen ${LISTEN} -seconds ${SERVER_SECONDS} >/dev/null 2>&1 < /dev/null &"
server_started=1
sleep 1
if remote_ssh "ss -Hltn | grep -q ':${BENCH_PORT} '"; then
	printf 'bench server listens on %s\n' "$LISTEN"
else
	printf 'the bench server did not start\n'
	exit 1
fi

log "build the rig image"
podman build -t "$IMAGE" -f "$DIR/Containerfile" "$DIR" >/dev/null

log "start the rig"
podman run -d --name "$CONTAINER" \
	--systemd=always \
	--device /dev/net/tun \
	--cap-add NET_ADMIN --cap-add NET_RAW --cap-add SYS_ADMIN \
	-v "${TSGW_SOCK}:${TSGW_SOCK}" \
	-v "${ROOT}/bin/tj:/usr/local/bin/tj:ro" \
	-v "${ROOT}/bin/bench:/usr/local/bin/tj-bench:ro" \
	"$IMAGE" >/dev/null
for _ in $(seq 1 30); do
	if rig systemctl is-system-running >/dev/null 2>&1; then break; fi
	sleep 1
done

for transport in $TRANSPORTS; do
	log "transport ${transport}: tj connect ${REF} --dns none --transport ${transport}"
	start="$(date +%s%N)"
	rig_connect "$REF" --user "$USER_" --dns none --transport "$transport"
	end="$(date +%s%N)"
	connect_s="$(awk -v s="$start" -v e="$end" 'BEGIN { printf "%.1f", (e - s) / 1e9 }')"
	status_line="$(rig tj status | grep -E '^Transport:' | sed -E 's/^Transport:[[:space:]]+//')"
	printf 'connect took %s s, status transport: %s\n' "$connect_s" "$status_line"

	shape_on

	connect_p50="-"
	connect_p95="-"
	connect_failed="-"
	if printf '%s' "$MEASURES" | grep -qw connect; then
		log "transport ${transport}: ${CONNECT_N} TCP connects to ${RESOLVER}:53 through the session"
		out="$(rig tj-bench connect -addr "${RESOLVER}:53" -n "$CONNECT_N" -timeout 8s)"
		printf '%s\n' "$out"
		connect_p50="$(printf '%s' "$out" | sed -E 's/.*p50_ms=([0-9.]+).*/\1/')"
		connect_p95="$(printf '%s' "$out" | sed -E 's/.*p95_ms=([0-9.]+).*/\1/')"
		connect_failed="$(printf '%s' "$out" | sed -E 's/.*failed=([0-9]+).*/\1/')"
	fi

	dns_ok="-"
	if printf '%s' "$MEASURES" | grep -qw dns; then
		log "transport ${transport}: ${DNS_N} UDP DNS queries to ${RESOLVER} with a 2 s limit"
		n=0
		for _ in $(seq 1 "$DNS_N"); do
			if rig dig +time=2 +tries=1 +short "@${RESOLVER}" "$DNS_NAME" 2>/dev/null | grep -qE '^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$'; then
				n=$((n + 1))
			fi
		done
		dns_ok="${n}/${DNS_N}"
		printf 'answered within 2 s: %s\n' "$dns_ok"
	fi

	bulk4="-"
	bulk6="-"
	bulk4_runs=""
	bulk6_runs=""
	if printf '%s' "$MEASURES" | grep -qw bulk; then
		log "transport ${transport}: ${BULK_RUNS} downloads of ${BULK_MIB} MiB over IPv4, ${BULK_TIMEOUT} s each at most"
		for _ in $(seq 1 "$BULK_RUNS"); do
			out="$(rig tj-bench get -addr "${REMOTE_V4}:${BENCH_PORT}" -mib "$BULK_MIB" -timeout "${BULK_TIMEOUT}s")"
			printf '%s\n' "$out"
			bulk4_runs="${bulk4_runs} $(printf '%s' "$out" | sed -E 's/.*mbit=([0-9.]+).*/\1/')"
		done
		bulk4="$(printf '%s\n' $bulk4_runs | median)"
		if [ -n "$REMOTE_V6" ]; then
			log "transport ${transport}: ${BULK_RUNS} downloads of ${BULK_MIB} MiB over IPv6"
			for _ in $(seq 1 "$BULK_RUNS"); do
				out="$(rig tj-bench get -addr "[${REMOTE_V6}]:${BENCH_PORT}" -mib "$BULK_MIB" -timeout "${BULK_TIMEOUT}s")"
				printf '%s\n' "$out"
				bulk6_runs="${bulk6_runs} $(printf '%s' "$out" | sed -E 's/.*mbit=([0-9.]+).*/\1/')"
			done
			bulk6="$(printf '%s\n' $bulk6_runs | median)"
		fi
	fi

	shape_off
	journal_notes="$(rig journalctl -u tj-session --no-pager 2>/dev/null | grep -iE 'buffer size|gso' | sed -E 's/^.*tj\[[0-9]+\]: //' | sort -u || true)"
	if [ -n "$journal_notes" ]; then
		printf 'journal notes:\n%s\n' "$journal_notes"
	fi
	rig tj disconnect
	sleep 2

	log "transport ${transport}: result${LABEL:+ (${LABEL})}"
	printf '| Measure | %s, loss %s%%, delay %s ms |\n' "$transport" "$LOSS" "$DELAY"
	printf '| -- | -- |\n'
	printf '| Connect wall time | %s s |\n' "$connect_s"
	printf '| TCP connect p50 / p95 / failed of %s | %s ms / %s ms / %s |\n' "$CONNECT_N" "$connect_p50" "$connect_p95" "$connect_failed"
	printf '| UDP DNS answered within 2 s | %s |\n' "$dns_ok"
	printf '| Bulk IPv4 median Mbit/s (runs:%s) | %s |\n' "$bulk4_runs" "$bulk4"
	printf '| Bulk IPv6 median Mbit/s (runs:%s) | %s |\n' "$bulk6_runs" "$bulk6"
	printf 'RESULT target=%s transport=%s controller=%s loss=%s delay=%s label=%q connect_s=%s p50=%s p95=%s failed=%s dns=%s bulk4=%s bulk6=%s\n' \
		"$TARGET" "$transport" "${TJ_QUIC_CONTROLLER:-default}" "$LOSS" "$DELAY" "$LABEL" "$connect_s" "$connect_p50" "$connect_p95" "$connect_failed" "$dns_ok" "$bulk4" "$bulk6"
done

log "confirm the remote is clean"
remote_ssh 'pkill -f tj-bench; true' >/dev/null 2>&1 || true
server_started=0
LEFT="$(remote_ssh 'ls -A "$HOME/.cache/tj" 2>/dev/null; ss -Hltn | grep ":'"${BENCH_PORT}"' " ; pgrep -af tj-bench | grep -v pgrep' || true)"
if [ -z "$LEFT" ]; then
	printf 'no bench file, listener, or process remains on the remote\n'
else
	printf 'the remote is not clean: %s\n' "$LEFT"
	exit 1
fi
