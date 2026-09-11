#!/usr/bin/env bash
# run.sh drives the tj e2e test against the shared gateway inside a rootless
# podman rig. It builds tj, builds and starts the rig, connects with --dns
# none, proves TCP and UDP reach the VPC over IPv4 and IPv6, checks the
# one-session lock, disconnects, and confirms the remote is clean. It then
# proves the DNS modes: --dns all against the discovered resolver, and
# --dns split against a temporary manifest it places on the gateway and
# removes again.
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
if rig tj status | tee /dev/stderr | grep -q "Status:.*up"; then
	ok "status shows the session up"
else
	bad "status does not show the session up"
fi

log "reachability: TCP over IPv4 to the VPC resolver ${TJ_TEST_RESOLVER}:53"
if rig nc -z -w 8 "$TJ_TEST_RESOLVER" 53; then
	ok "TCP IPv4 to ${TJ_TEST_RESOLVER}:53"
else
	bad "TCP IPv4 to ${TJ_TEST_RESOLVER}:53"
fi

log "reachability: UDP DNS over IPv4 to ${TJ_TEST_RESOLVER}"
if rig dig +time=5 +tries=2 +short "@${TJ_TEST_RESOLVER}" "$TJ_TEST_DNS_NAME" | tee /dev/stderr | grep -qE '[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+'; then
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

log "remove the temporary manifest and confirm the gateway is clean"
remove_gateway_manifest
GW_LEFT="$(gateway_ssh \
	'ls -A /etc/tj 2>/dev/null; ls -A "$HOME/.cache/tj" 2>/dev/null; ls -A /run/user/0/tj-helper.* 2>/dev/null' || true)"
if [ -z "$GW_LEFT" ]; then
	ok "the gateway has no manifest and no tj file"
else
	bad "the gateway is not clean: ${GW_LEFT}"
fi

log "result: ${pass} passed, ${fail} failed"
[ "$fail" -eq 0 ]
