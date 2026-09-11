#!/bin/sh
# tj discovery script. POSIX sh only, using sh, ip, cat, base64, uname,
# mkdir, chmod, rm, and curl. Prints one JSON document on stdout. Every
# value here is a CIDR, an address, a hostname, a path, or base64, so no
# JSON string escaping is needed.
set -eu

TJ_VERSION="1"

# ---- small JSON scanners for ip -j output -------------------------------
# These are not general JSON parsers. ip's compact -j output never nests an
# escaped quote inside a value this script reads, so a literal scan for
# "key":"value" and "key":<digits> is enough.

json_strings() {
	__json=$1
	__key=$2
	__needle="\"$__key\":\""
	__rest=$__json
	while :; do
		case $__rest in
			*"$__needle"*) ;;
			*) break ;;
		esac
		__rest=${__rest#*"$__needle"}
		__val=${__rest%%\"*}
		printf '%s\n' "$__val"
		__rest=${__rest#*\"}
	done
}

addr_pairs() {
	__json=$1
	__rest=$__json
	while :; do
		case $__rest in
			*'"local":"'*) ;;
			*) break ;;
		esac
		__rest=${__rest#*'"local":"'}
		__ip=${__rest%%\"*}
		__rest=${__rest#*\"}
		case $__rest in
			*'"prefixlen":'*) ;;
			*) break ;;
		esac
		__rest=${__rest#*'"prefixlen":'}
		__plen=""
		while :; do
			__c=${__rest%"${__rest#?}"}
			case $__c in
				[0-9]) __plen="$__plen$__c"; __rest=${__rest#?} ;;
				*) break ;;
			esac
		done
		printf '%s/%s\n' "$__ip" "$__plen"
	done
}

join_quoted() {
	__first=1
	__out=""
	for __v in "$@"; do
		[ -n "$__v" ] || continue
		if [ "$__first" -eq 1 ]; then
			__out="\"$__v\""
			__first=0
		else
			__out="$__out,\"$__v\""
		fi
	done
	printf '%s' "$__out"
}

# is_cidr accepts a token that has the shape of an IPv4 or IPv6 CIDR. It
# rejects anything else, so a metadata endpoint that answers with an HTML
# error page on a non-AWS host cannot inject garbage into the JSON.
is_cidr() {
	case $1 in
		*[!0-9a-fA-F.:/]*) return 1 ;;
	esac
	case $1 in
		*/*) ;;
		*) return 1 ;;
	esac
	__mask=${1##*/}
	case $__mask in
		''|*[!0-9]*) return 1 ;;
	esac
	[ -n "${1%/*}" ] || return 1
	return 0
}

keep_cidrs() {
	__k_out=""
	for __k_t in "$@"; do
		if is_cidr "$__k_t"; then
			__k_out="$__k_out $__k_t"
		fi
	done
	printf '%s' "$__k_out"
}

# ---- exec_dir ------------------------------------------------------------
# A write-and-chmod probe is not a real execute test: both succeed on a
# noexec mount. sh cannot compile a binary to prove execute permission, so
# this only proves write access. The client verifies real execute
# permission later, by running the uploaded helper binary.
exec_dir=""
for candidate in "${XDG_RUNTIME_DIR:-}" "${HOME:-}/.cache/tj"; do
	[ -n "$candidate" ] || continue
	existed=1
	[ -d "$candidate" ] || existed=0
	if ! mkdir -p "$candidate" 2>/dev/null; then
		continue
	fi
	probe="$candidate/.tj-probe.$$"
	ok=0
	if (: > "$probe") 2>/dev/null && chmod 0700 "$probe" 2>/dev/null; then
		ok=1
	fi
	rm -f "$probe" 2>/dev/null
	# Discovery only proves exec_dir; it uploads nothing. Remove a
	# directory this run created, so a describe or doctor run leaves the
	# remote exactly as it found it.
	if [ "$existed" -eq 0 ]; then
		rmdir "$candidate" 2>/dev/null || true
	fi
	if [ "$ok" -eq 1 ]; then
		exec_dir=$candidate
		break
	fi
done

# ---- host facts ------------------------------------------------------------
hostname=$(uname -n)
uname_m=$(uname -m)

# ---- manifest ----------------------------------------------------------
config_home="${XDG_CONFIG_HOME:-${HOME:-}/.config}"
manifest_path=""
manifest_b64=""
for candidate in "$config_home/tj/manifest.yaml" /etc/tj/manifest.yaml; do
	if [ -r "$candidate" ]; then
		manifest_path=$candidate
		manifest_b64=$(base64 -w0 < "$candidate")
		break
	fi
done

# ---- link routes: connected subnets, IPv4 and IPv6 ------------------------
route4_json=$(ip -j route 2>/dev/null || echo '[]')
route6_json=$(ip -j -6 route 2>/dev/null || echo '[]')
link_routes=""
for dst in $(json_strings "$route4_json" dst) $(json_strings "$route6_json" dst); do
	case $dst in
		*/*) link_routes="$link_routes $dst" ;;
	esac
done
link_routes_json=$(join_quoted $link_routes)

# ---- interface addresses, informational -----------------------------------
addr_json=$(ip -j addr show scope global 2>/dev/null || echo '[]')
addresses_json=$(join_quoted $(addr_pairs "$addr_json"))

# ---- resolvers and search domains ------------------------------------------
resolv_file=/run/systemd/resolve/resolv.conf
[ -r "$resolv_file" ] || resolv_file=/etc/resolv.conf

resolvers=""
search_domains=""
if [ -r "$resolv_file" ]; then
	while IFS= read -r line || [ -n "$line" ]; do
		case $line in
			"nameserver "*)
				ns=${line#nameserver }
				case $ns in
					100.100.100.100|fd7a:115c:a1e0::53) continue ;;
				esac
				resolvers="$resolvers $ns"
				;;
			"search "*)
				search_domains="$search_domains ${line#search }"
				;;
		esac
	done < "$resolv_file"
fi
resolvers_json=$(join_quoted $resolvers)
search_domains_json=$(join_quoted $search_domains)

# ---- cloud metadata: AWS IMDSv2, 1s timeout --------------------------------
cloud_provider=""
cloud_networks_json=""
if command -v curl >/dev/null 2>&1; then
	token=$(curl -s -m 1 -X PUT -H "X-aws-ec2-metadata-token-ttl-seconds: 60" \
		http://169.254.169.254/latest/api/token 2>/dev/null || true)
	if [ -n "$token" ]; then
		mac=""
		for m in $(curl -s -m 1 -H "X-aws-ec2-metadata-token: $token" \
			http://169.254.169.254/latest/meta-data/network/interfaces/macs/ 2>/dev/null || true); do
			mac=${m%/}
			break
		done
		if [ -n "$mac" ]; then
			v4=$(curl -s -m 1 -H "X-aws-ec2-metadata-token: $token" \
				"http://169.254.169.254/latest/meta-data/network/interfaces/macs/$mac/vpc-ipv4-cidr-blocks" 2>/dev/null || true)
			v6=$(curl -s -m 1 -H "X-aws-ec2-metadata-token: $token" \
				"http://169.254.169.254/latest/meta-data/network/interfaces/macs/$mac/vpc-ipv6-cidr-blocks" 2>/dev/null || true)
			cloud_nets=$(keep_cidrs $v4 $v6)
			if [ -n "$cloud_nets" ]; then
				cloud_provider="aws"
				cloud_networks_json=$(join_quoted $cloud_nets)
			fi
		fi
	fi
fi

# ---- emit strict JSON -------------------------------------------------------
printf '{'
printf '"version":"%s",' "$TJ_VERSION"
printf '"hostname":"%s",' "$hostname"
printf '"uname_m":"%s",' "$uname_m"
printf '"exec_dir":"%s",' "$exec_dir"
if [ -n "$manifest_path" ]; then
	printf '"manifest_path":"%s",' "$manifest_path"
	printf '"manifest":"%s",' "$manifest_b64"
fi
printf '"addresses":[%s],' "$addresses_json"
printf '"link_routes":[%s],' "$link_routes_json"
printf '"resolvers":[%s],' "$resolvers_json"
printf '"search_domains":[%s]' "$search_domains_json"
if [ -n "$cloud_provider" ]; then
	printf ',"cloud":{"provider":"%s","networks":[%s]}' "$cloud_provider" "$cloud_networks_json"
fi
printf '}\n'
