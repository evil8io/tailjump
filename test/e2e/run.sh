#!/usr/bin/env bash
# run.sh drives the tj e2e test against the shared gateway inside a rootless
# podman rig. It builds tj, builds and starts the rig, connects with --dns
# none, proves the session runs on the QUIC transport with the helper port
# bound to the tailnet address only, proves TCP and UDP reach the VPC over
# IPv4 and IPv6, checks the one-session lock, disconnects, and confirms the
# remote is clean. It then proves the DNS modes: --dns all against the
# discovered resolver, and --dns split against a temporary manifest it places
# on the gateway and removes again. It then blocks the QUIC port range inside
# the rig and proves the fallback to the SSH transport, and that --transport
# quic fails without a session. With TJ_TEST_DERP_REF set it also connects
# once to a DERP-relayed remote on the QUIC transport.
#
# It never runs tj connect on the host: the host holds its own session and the
# one-session rule forbids a second. The rig reaches the tailnet through the
# host's tailscaled socket.
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
: "${TJ_TEST_SEARCH_DOMAIN:?set TJ_TEST_SEARCH_DOMAIN (a private DNS domain the VPC resolver answers, for the split DNS check) in target.env or the environment}"
TJ_TEST_USER="${TJ_TEST_USER:-root}"
TJ_TEST_DNS_NAME="${TJ_TEST_DNS_NAME:-amazon.com}"
QUIC_PORTS="${TJ_TEST_QUIC_PORTS:-7443-7452}"
QUIC_PORT_RE='74(4[3-9]|5[0-2])'

TSGW_SOCK="/var/run/tailscale/tailscaled.sock"
IMAGE="tj-e2e:latest"
CONTAINER="tj-e2e-$$"
SSH_OPTS=(-o StrictHostKeyChecking=accept-new -o ConnectTimeout=15 -o BatchMode=yes)

pass=0
fail=0
gateway_manifest_written=0

log() { printf '\n=== %s ===\n' "$*"; }
ok() { printf 'PASS: %s\n' "$*"; pass=$((pass + 1)); }
bad() { printf 'FAIL: %s\n' "$*"; fail=$((fail + 1)); }

rig() { podman exec "$CONTAINER" "$@"; }

gateway_ssh() { ssh "${SSH_OPTS[@]}" "root@${TJ_TEST_REMOTE}" "$@"; }

# status_transport prints the Transport line of tj status without the label.
status_transport() {
	rig tj status | grep -E '^Transport:' | sed -E 's/^Transport:[[:space:]]+//'
}

# remote_quic_listeners prints the UDP listeners of the remote in the QUIC
# port range, one per line, as "address:port".
remote_quic_listeners() {
	ssh "${SSH_OPTS[@]}" "root@$1" 'ss -Hlun' 2>/dev/null | awk '{print $4}' | grep -E ":${QUIC_PORT_RE}\$" || true
}

# connect_seconds runs tj connect with the given arguments and prints the
# wall time in seconds with one decimal.
connect_seconds() {
	local start end
	start="$(date +%s%N)"
	rig tj -v connect "$@" >&2
	end="$(date +%s%N)"
	awk -v s="$start" -v e="$end" 'BEGIN { printf "%.1f", (e - s) / 1e9 }'
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

# query_link prints the resolved link name that answered a resolvectl query,
# so a test can tell a remote answer (link tj0) from a local one.
query_link() {
	rig resolvectl query "$1" 2>/dev/null | grep -oE 'link: [^[:space:]]+' | head -1 | cut -d' ' -f2
}

# tj0_present reports whether resolvectl still lists the tj device, so a
# test can confirm disconnect reverted the DNS configuration.
tj0_present() {
	rig resolvectl status 2>/dev/null | grep -q '(tj0)'
}

write_gateway_manifest() {
	gateway_ssh "mkdir -p /etc/tj && cat > /etc/tj/manifest.yaml" <<-MANIFEST
	version: 1
	name: test
	dns:
	  servers: [${TJ_TEST_RESOLVER}]
	  domains: [${TJ_TEST_SEARCH_DOMAIN}]
	MANIFEST
	gateway_manifest_written=1
}

remove_gateway_manifest() {
	gateway_ssh 'rm -rf /etc/tj' >/dev/null 2>&1 || true
	gateway_manifest_written=0
}

cleanup() {
	log "cleanup"
	if [ "$gateway_manifest_written" -eq 1 ]; then
		remove_gateway_manifest
	fi
	podman rm -f "$CONTAINER" >/dev/null 2>&1 || true
}
trap cleanup EXIT

log "build tj (host arch) with the embedded helpers"
(cd "$ROOT" && task build)

log "probe the gateway VPC endpoints over Tailscale SSH"
GW_V6="$(gateway_ssh \
	"ip -6 -br addr show ens5 scope global | awk '{print \$3}' | head -1 | cut -d/ -f1" || true)"
if [ -n "$GW_V6" ]; then
	printf 'gateway VPC IPv6: %s\n' "$GW_V6"
else
	printf 'gateway has no VPC IPv6; the IPv6 TCP test is skipped\n'
fi

log "look up the gateway's own private DNS name, for the split DNS check"
GW_PRIVATE_NAME="$(gateway_ssh 'hostname -f')"
printf 'gateway private DNS name: %s\n' "$GW_PRIVATE_NAME"
case "$GW_PRIVATE_NAME" in
*".$TJ_TEST_SEARCH_DOMAIN") ;;
*)
	printf 'the gateway private DNS name %s is not under %s\n' "$GW_PRIVATE_NAME" "$TJ_TEST_SEARCH_DOMAIN"
	exit 1
	;;
esac

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
printf 'rig connected subnets:\n'
rig ip -br addr show || true

log "tj connect ${TJ_TEST_REF} --dns none"
rig tj -v connect "$TJ_TEST_REF" --user "$TJ_TEST_USER" --dns none

log "tj status"
if rig tj status | tee >(cat >&2) | grep -q "Status:.*up"; then
	ok "status shows the session up"
else
	bad "status does not show the session up"
fi

log "transport: the session runs on quic with a port from ${QUIC_PORTS}"
TRANSPORT="$(status_transport)"
QUIC_PORT="$(printf '%s' "$TRANSPORT" | grep -oE 'port [0-9]+' | awk '{print $2}')"
if printf '%s' "$TRANSPORT" | grep -qE "^quic \(port ${QUIC_PORT_RE}\)"; then
	ok "status shows transport ${TRANSPORT}"
else
	bad "status shows transport '${TRANSPORT}', want quic with a port from ${QUIC_PORTS}"
fi
if rig tj status --json | grep -q "\"transport\":\"quic\""; then
	ok "status --json has transport quic"
else
	bad "status --json lacks transport quic"
fi

log "transport: the helper listener is bound to the tailnet address only"
LISTENERS="$(remote_quic_listeners "$TJ_TEST_REMOTE")"
printf 'remote udp listeners in the range:\n%s\n' "${LISTENERS:-<none>}"
if [ -n "$QUIC_PORT" ] && printf '%s\n' "$LISTENERS" | grep -qx "${TJ_TEST_REMOTE}:${QUIC_PORT}"; then
	ok "the remote listens on ${TJ_TEST_REMOTE}:${QUIC_PORT}"
else
	bad "the remote does not listen on ${TJ_TEST_REMOTE}:${QUIC_PORT:-?}"
fi
if printf '%s\n' "$LISTENERS" | grep -qE "^(\*|0\.0\.0\.0|\[::\]):"; then
	bad "a helper listener is bound to the wildcard address"
else
	ok "no helper listener is bound to the wildcard address"
fi

log "reachability: TCP over IPv4 to the VPC resolver ${TJ_TEST_RESOLVER}:53"
if rig nc -z -w 8 "$TJ_TEST_RESOLVER" 53; then
	ok "TCP IPv4 to ${TJ_TEST_RESOLVER}:53"
else
	bad "TCP IPv4 to ${TJ_TEST_RESOLVER}:53"
fi

log "reachability: UDP DNS over IPv4 to ${TJ_TEST_RESOLVER}"
if rig dig +time=5 +tries=2 +short "@${TJ_TEST_RESOLVER}" "$TJ_TEST_DNS_NAME" | tee >(cat >&2) | grep -qE '[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+'; then
	ok "UDP DNS IPv4 to ${TJ_TEST_RESOLVER} resolved ${TJ_TEST_DNS_NAME}"
else
	bad "UDP DNS IPv4 to ${TJ_TEST_RESOLVER}"
fi

if [ -n "$GW_V6" ]; then
	log "reachability: TCP over IPv6 to the VPC endpoint [${GW_V6}]:22"
	if rig nc -6 -z -w 8 "$GW_V6" 22; then
		ok "TCP IPv6 to [${GW_V6}]:22"
	else
		bad "TCP IPv6 to [${GW_V6}]:22"
	fi
fi

log "one-session lock: a second connect must refuse"
if rig tj connect "$TJ_TEST_REF" --user "$TJ_TEST_USER" --dns none; then
	bad "the second connect did not refuse"
else
	code=$?
	if [ "$code" -eq 3 ]; then
		ok "the second connect refused with exit code 3"
	else
		bad "the second connect failed with exit ${code}, want 3"
	fi
fi

log "tj disconnect"
rig tj disconnect
sleep 2
status_after="$(rig tj status)"
printf '%s\n' "$status_after"
if printf '%s' "$status_after" | grep -q "no active session"; then
	ok "status shows no session after disconnect"
else
	bad "status still shows a session after disconnect"
fi

log "confirm no file remains on the remote"
LEFT="$(gateway_ssh \
	'ls -A "$HOME/.cache/tj" 2>/dev/null; ls -A /run/user/0/tj-helper.* 2>/dev/null' || true)"
if [ -z "$LEFT" ]; then
	ok "no tj file remains on the remote"
else
	bad "files remain on the remote: ${LEFT}"
fi

log "confirm no helper listener remains on the remote"
LISTENERS="$(remote_quic_listeners "$TJ_TEST_REMOTE")"
if [ -z "$LISTENERS" ]; then
	ok "no udp listener in ${QUIC_PORTS} remains on the remote"
else
	bad "udp listeners remain on the remote: ${LISTENERS}"
fi

log "tj connect ${TJ_TEST_REF} --dns all (no manifest, servers default to the discovered resolver)"
rig tj -v connect "$TJ_TEST_REF" --user "$TJ_TEST_USER" --dns all

log "resolvectl status tj0: all mode routes every query with the default route on"
STATUS_ALL="$(rig resolvectl status tj0)"
printf '%s\n' "$STATUS_ALL"
if printf '%s' "$STATUS_ALL" | grep -q "DNS Domain: ~\." && printf '%s' "$STATUS_ALL" | grep -q "Default Route: yes"; then
	ok "tj0 has the ~. domain and the default route on"
else
	bad "tj0 does not show the all-mode domain and default route"
fi

log "a public name resolves through the remote resolver over the tunnel"
LINK="$(query_link "$TJ_TEST_DNS_NAME")"
printf 'resolved %s via link %s\n' "$TJ_TEST_DNS_NAME" "$LINK"
if [ "$LINK" = "tj0" ]; then
	ok "${TJ_TEST_DNS_NAME} resolved via tj0"
else
	bad "${TJ_TEST_DNS_NAME} resolved via ${LINK:-<none>}, want tj0"
fi

log "tj disconnect"
rig tj disconnect
sleep 2
if tj0_present; then
	bad "tj0 still present in resolvectl status after disconnect"
else
	ok "tj0 is gone from resolvectl status after disconnect"
fi

log "write a temporary manifest on the gateway for the split DNS check"
write_gateway_manifest
gateway_ssh 'cat /etc/tj/manifest.yaml'

log "tj connect ${TJ_TEST_REF} --dns split"
rig tj -v connect "$TJ_TEST_REF" --user "$TJ_TEST_USER" --dns split

log "resolvectl status tj0: split mode routes only the manifest domain, default route off"
STATUS_SPLIT="$(rig resolvectl status tj0)"
printf '%s\n' "$STATUS_SPLIT"
if printf '%s' "$STATUS_SPLIT" | grep -q "DNS Domain: ~${TJ_TEST_SEARCH_DOMAIN}" \
	&& ! printf '%s' "$STATUS_SPLIT" | grep -q '~\.' \
	&& printf '%s' "$STATUS_SPLIT" | grep -q "Default Route: no"; then
	ok "tj0 has only the ~${TJ_TEST_SEARCH_DOMAIN} domain and the default route off"
else
	bad "tj0 does not show exactly the split-mode domain with the default route off"
fi

log "a private name under ${TJ_TEST_SEARCH_DOMAIN} resolves through the remote"
PRIVATE_LINK="$(query_link "$GW_PRIVATE_NAME")"
printf 'resolved %s via link %s\n' "$GW_PRIVATE_NAME" "$PRIVATE_LINK"
if [ "$PRIVATE_LINK" = "tj0" ]; then
	ok "${GW_PRIVATE_NAME} resolved via tj0"
else
	bad "${GW_PRIVATE_NAME} resolved via ${PRIVATE_LINK:-<none>}, want tj0"
fi

log "a public name still resolves through the rig's own resolver, not the remote"
PUBLIC_LINK="$(query_link "$TJ_TEST_DNS_NAME")"
printf 'resolved %s via link %s\n' "$TJ_TEST_DNS_NAME" "$PUBLIC_LINK"
if [ -n "$PUBLIC_LINK" ] && [ "$PUBLIC_LINK" != "tj0" ]; then
	ok "${TJ_TEST_DNS_NAME} resolved via ${PUBLIC_LINK}, not tj0"
else
	bad "${TJ_TEST_DNS_NAME} resolved via ${PUBLIC_LINK:-<none>}, want a link other than tj0"
fi

log "tj disconnect"
rig tj disconnect
sleep 2
if tj0_present; then
	bad "tj0 still present in resolvectl status after disconnect"
else
	ok "tj0 is gone from resolvectl status after disconnect"
fi

log "remove the temporary manifest"
remove_gateway_manifest

log "fallback: measure two connects on the ssh transport, the smaller one is the baseline"
SSH_SECONDS=""
for run in 1 2; do
	seconds="$(connect_seconds "$TJ_TEST_REF" --user "$TJ_TEST_USER" --dns none --transport ssh)"
	printf 'connect --transport ssh run %s took %s s\n' "$run" "$seconds"
	if [ "$run" -eq 1 ]; then
		TRANSPORT="$(status_transport)"
		if [ "$TRANSPORT" = "ssh" ]; then
			ok "status shows transport ssh for --transport ssh"
		else
			bad "status shows transport '${TRANSPORT}', want ssh"
		fi
	fi
	rig tj disconnect
	sleep 2
	if [ -z "$SSH_SECONDS" ] || awk -v a="$seconds" -v b="$SSH_SECONDS" 'BEGIN { exit !(a < b) }'; then
		SSH_SECONDS="$seconds"
	fi
done

log "fallback: block udp ${QUIC_PORTS} inside the rig, then connect with the auto transport"
block_quic_range
rig nft list table inet tjtest
FALLBACK_SECONDS="$(connect_seconds "$TJ_TEST_REF" --user "$TJ_TEST_USER" --dns none)"
printf 'connect with the range blocked took %s s, the ssh baseline took %s s\n' "$FALLBACK_SECONDS" "$SSH_SECONDS"
# The extra time is the 5 s handshake budget plus the run-to-run variance of
# a connect, measured at 1.3 s between two ssh-transport runs, so the bound
# is 8 s.
if awk -v f="$FALLBACK_SECONDS" -v s="$SSH_SECONDS" 'BEGIN { exit !(f - s <= 8.0) }'; then
	ok "the fallback came up within 8 s of the ssh baseline"
else
	bad "the fallback took ${FALLBACK_SECONDS} s against a ${SSH_SECONDS} s baseline, want at most 8 s extra"
fi
TRANSPORT="$(status_transport)"
if printf '%s' "$TRANSPORT" | grep -q '^ssh (fallback: '; then
	ok "status shows transport ${TRANSPORT}"
else
	bad "status shows transport '${TRANSPORT}', want ssh with a fallback reason"
fi
journal="$(rig journalctl -u tj-session --no-pager 2>/dev/null || true)"
if printf '%s' "$journal" | grep -q "quic transport unavailable" && printf '%s' "$journal" | grep -q "udp:${QUIC_PORTS}"; then
	ok "the journal has the fallback warning with the policy rule udp:${QUIC_PORTS}"
else
	bad "the journal lacks the fallback warning with the policy rule"
	printf '%s\n' "$journal" | tail -n 20
fi

log "fallback: TCP and UDP DNS still reach the VPC on the ssh transport"
if rig nc -z -w 8 "$TJ_TEST_RESOLVER" 53; then
	ok "TCP IPv4 to ${TJ_TEST_RESOLVER}:53 on the ssh transport"
else
	bad "TCP IPv4 to ${TJ_TEST_RESOLVER}:53 on the ssh transport"
fi
if rig dig +time=5 +tries=2 +short "@${TJ_TEST_RESOLVER}" "$TJ_TEST_DNS_NAME" | grep -qE '[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+'; then
	ok "UDP DNS IPv4 to ${TJ_TEST_RESOLVER} on the ssh transport"
else
	bad "UDP DNS IPv4 to ${TJ_TEST_RESOLVER} on the ssh transport"
fi
rig tj disconnect
sleep 2
LISTENERS="$(remote_quic_listeners "$TJ_TEST_REMOTE")"
if [ -z "$LISTENERS" ]; then
	ok "no udp listener in ${QUIC_PORTS} remains on the remote after the fallback session"
else
	bad "udp listeners remain on the remote after the fallback session: ${LISTENERS}"
fi

log "fallback: with the range blocked, --transport quic must fail without a session"
if rig tj connect "$TJ_TEST_REF" --user "$TJ_TEST_USER" --dns none --transport quic; then
	bad "connect --transport quic succeeded with the range blocked"
	rig tj disconnect
else
	code=$?
	if [ "$code" -ne 0 ] && [ "$code" -ne 3 ]; then
		ok "connect --transport quic failed with exit ${code}"
	else
		bad "connect --transport quic exited ${code}, want non-zero and not 3"
	fi
fi
sleep 2
status_after="$(rig tj status)"
if printf '%s' "$status_after" | grep -q "no active session"; then
	ok "no session remains after the failed --transport quic connect"
else
	bad "a session remains after the failed --transport quic connect: ${status_after}"
fi
unblock_quic_range

if [ -n "${TJ_TEST_DERP_REF:-}" ]; then
	log "relayed remote: tj connect ${TJ_TEST_DERP_REF} --dns none over DERP"
	rig tj -v connect "$TJ_TEST_DERP_REF" --user root --dns none
	TRANSPORT="$(status_transport)"
	if printf '%s' "$TRANSPORT" | grep -qE "^quic \(port ${QUIC_PORT_RE}\)"; then
		ok "relayed remote: status shows transport ${TRANSPORT}"
	else
		bad "relayed remote: status shows transport '${TRANSPORT}', want quic"
	fi
	if rig nc -z -w 8 "$TJ_TEST_DERP_RESOLVER" 53; then
		ok "relayed remote: TCP IPv4 to ${TJ_TEST_DERP_RESOLVER}:53"
	else
		bad "relayed remote: TCP IPv4 to ${TJ_TEST_DERP_RESOLVER}:53"
	fi
	if rig dig +time=5 +tries=2 +short "@${TJ_TEST_DERP_RESOLVER}" "$TJ_TEST_DNS_NAME" | grep -qE '[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+'; then
		ok "relayed remote: UDP DNS IPv4 to ${TJ_TEST_DERP_RESOLVER}"
	else
		bad "relayed remote: UDP DNS IPv4 to ${TJ_TEST_DERP_RESOLVER}"
	fi
	rig tj disconnect
	sleep 2
	DERP_LEFT="$(ssh "${SSH_OPTS[@]}" "root@${TJ_TEST_DERP_REMOTE}" \
		'ls -A "$HOME/.cache/tj" 2>/dev/null; ls -A /run/user/0/tj-helper.* 2>/dev/null' || true)"
	DERP_LISTENERS="$(remote_quic_listeners "$TJ_TEST_DERP_REMOTE")"
	if [ -z "$DERP_LEFT" ] && [ -z "$DERP_LISTENERS" ]; then
		ok "relayed remote: no file and no listener remains"
	else
		bad "relayed remote: not clean: files '${DERP_LEFT}' listeners '${DERP_LISTENERS}'"
	fi
fi

log "confirm the gateway is clean"
GW_LEFT="$(gateway_ssh \
	'ls -A /etc/tj 2>/dev/null; ls -A "$HOME/.cache/tj" 2>/dev/null; ls -A /run/user/0/tj-helper.* 2>/dev/null' || true)"
if [ -z "$GW_LEFT" ]; then
	ok "the gateway has no manifest and no tj file"
else
	bad "the gateway is not clean: ${GW_LEFT}"
fi

log "result: ${pass} passed, ${fail} failed"
[ "$fail" -eq 0 ]
