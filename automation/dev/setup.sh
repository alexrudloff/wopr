#!/usr/bin/env bash
# Prepare a WOPR development environment, or report what is missing.
#
#   make setup    runs this script
#   make doctor   runs it with --check, which reports and changes nothing
#
# Options:
#   --check            report every component and exit; install nothing
#   --background       run detached; progress goes to $WOPR_DEV_HOME/setup.log
#   --no-build         skip the final build of bin/wopr
#
# Everything installed goes under $WOPR_DEV_HOME (default ${XDG_CACHE_HOME:-~/.cache}/wopr-dev) or
# the checkout's ignored directories. The script never uses sudo. Make includes
# $WOPR_DEV_HOME/env.mk, so every make target uses the toolchain chosen here. Shells get the same values from `source $WOPR_DEV_HOME/env.sh`.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
dev_home="${WOPR_DEV_HOME:-${XDG_CACHE_HOME:-$HOME/.cache}/wopr-dev}"
check=0 background=0 build=1
for arg in "$@"; do
	case "$arg" in
	--check) check=1 ;;
	--background) background=1 ;;
	--no-build) build=0 ;;
	-h | --help)
		sed -n '4,/^set -euo pipefail/p' "${BASH_SOURCE[0]}" | sed -e '$d' -e 's/^# \{0,1\}//'
		exit 0
		;;
	*)
		echo "setup: unknown option: $arg (try --help)" >&2
		exit 2
		;;
	esac
done
mkdir -p "$dev_home"

have() { command -v "$1" >/dev/null 2>&1; }

if ((background)); then
	rest=()
	for arg in "$@"; do [[ $arg == --background ]] || rest+=("$arg"); done
	detach=(nohup)
	have setsid && detach=(setsid nohup) # A plain background job dies with the caller's session in some sandboxes.
	"${detach[@]}" "${BASH_SOURCE[0]}" ${rest[@]+"${rest[@]}"} >"$dev_home/setup.log" 2>&1 </dev/null &
	echo "$!" >"$dev_home/setup.pid"
	echo "setup: running in the background as pid $!"
	echo "setup: follow it with: tail -f $dev_home/setup.log (it ends with 'setup: done' or 'setup: FAILED')"
	exit 0
fi

summary=() missing=0
row() {
	summary+=("$(printf '  %-10s %-8s %s' "$1" "$2" "$3")")
	printf 'setup: %-10s %-8s %s\n' "$1" "$2" "$3"
}
need() {
	row "$1" missing "$2"
	missing=1
}
sha256() { if have sha256sum; then sha256sum "$1" | cut -d' ' -f1; else shasum -a 256 "$1" | cut -d' ' -f1; fi; }
reachable() { curl -fsS -m 8 -o /dev/null "$1" 2>/dev/null; }

# Another installer (mise, asdf, a shell profile) may export GOROOT or GOBIN
# for a different Go. A foreign GOROOT makes the pinned go report "package
# bufio is not in std", so setup ignores both and env.mk/env.sh clear them.
leaked=()
[[ -n ${GOROOT:-} ]] && leaked+=("GOROOT=$GOROOT")
[[ -n ${GOBIN:-} ]] && leaked+=("GOBIN=$GOBIN")
unset GOROOT GOBIN

# Go toolchain: the version named by go.mod's toolchain directive.
go_want="$(awk '$1 == "toolchain" { print $2; exit }' "$root/go.mod")"
[[ -n $go_want ]] || go_want="go$(awk '$1 == "go" { print $2; exit }' "$root/go.mod")"
os="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "$(uname -m)" in x86_64 | amd64) arch=amd64 ;; aarch64 | arm64) arch=arm64 ;; *) arch="$(uname -m)" ;; esac
pinned="$dev_home/toolchains/$go_want"
go_bin="" go_mode=""
goversion() { GOTOOLCHAIN=local "$1" env GOVERSION 2>/dev/null || true; }
find_go() {
	if [[ -x $pinned/bin/go && "$(goversion "$pinned/bin/go")" == "$go_want" ]]; then
		go_bin="$pinned/bin/go" go_mode=pinned
	elif have go && [[ "$(goversion go)" == "$go_want" ]]; then
		go_bin="$(command -v go)" go_mode=path
	elif ((!check)) && have go && [[ "$(cd "$root" && GOTOOLCHAIN=auto go env GOVERSION 2>/dev/null)" == "$go_want" ]]; then
		go_bin="$(command -v go)" go_mode=auto # go downloads go.mod's toolchain itself.
	fi
}
install_go() { # Called in a condition, so errexit is off here: every step checks its own status.
	local tmp url="" sum="" file="$go_want.$os-$arch.tar.gz" index top
	tmp="$(mktemp -d "${TMPDIR:-/tmp}/wopr.XXXXXXXX")"
	if index="$(curl -fsSL -m 20 'https://go.dev/dl/?mode=json&include=all' 2>/dev/null)"; then
		# Each file object in the index names its filename before its sha256.
		sum="$(tr -d '\n' <<<"$index" | grep -oE "\"filename\": *\"${file//./\\.}\"[^}]*" | grep -oE '"sha256": *"[0-9a-f]{64}"' | grep -oE '[0-9a-f]{64}' | head -n 1 || true)"
		[[ -n $sum ]] && url="https://go.dev/dl/$file"
	fi
	if [[ -z $url ]]; then
		read -r url sum < <(awk -v v="${go_want#go}" -v o="$os" -v a="$arch" \
			'!/^#/ && $1 == "go" && $2 == v && $3 == o && $4 == a { print $6, $7; exit }' "$root/automation/dev/toolchains.lock") || true
	fi
	if [[ -z $url || -z $sum ]]; then
		echo "setup: no verified download of $go_want for $os/$arch; install it from https://go.dev/dl/" >&2
		rm -rf "$tmp"
		return 1
	fi
	echo "setup: downloading $url"
	curl -fsSL -m 900 -o "$tmp/go.tar.gz" "$url" || { rm -rf "$tmp"; return 1; }
	[[ "$(sha256 "$tmp/go.tar.gz")" == "$sum" ]] || { echo "setup: checksum mismatch for $url" >&2; rm -rf "$tmp"; return 1; }
	if ! { mkdir -p "$tmp/x" && tar -xzf "$tmp/go.tar.gz" -C "$tmp/x"; }; then
		rm -rf "$tmp"
		return 1
	fi
	top="$tmp/x"
	[[ -x $top/go/bin/go ]] && top="$top/go" # go.dev archives nest under go/; actions/go-versions archives do not.
	rm -rf "$pinned" && mkdir -p "$(dirname "$pinned")" && mv "$top" "$pinned"
	rm -rf "$tmp"
}
find_go
if [[ -z $go_bin ]] && ((!check)) && install_go; then find_go; fi
if ((${#leaked[@]})); then
	row goenv warn "ignored ${leaked[*]} from another installer; make and env.sh unset them"
fi
if [[ -n $go_bin ]]; then
	row go ok "$go_want via $go_mode ($go_bin)"
else
	need go "$go_want not available; run make setup, or install it from https://go.dev/dl/"
fi

# Module downloads: the configured GOPROXY must be reachable; otherwise builds
# use the module cache.
if [[ -n $go_bin ]]; then
	# Read the user's own setting, not the local-proxy fallback that setup
	# once exported through env.mk (make passes it to this script).
	if [[ ${WOPR_GOPROXY_FALLBACK:-} == 1 ]]; then
		unset GOPROXY GOSUMDB GOWORK WOPR_GOPROXY_FALLBACK
	fi
	configured="$(cd "$root" && { GOTOOLCHAIN=local "$go_bin" env GOPROXY 2>/dev/null || true; })"
	# go env prints nothing when Go's config directory is unreadable, as in
	# some sandboxes; Go then uses its default, so check that.
	[[ -n $configured ]] || configured="https://proxy.golang.org,direct"
	first="${configured%%[,|]*}"
	if [[ $first == http* ]] && reachable "$first/golang.org/x/mod/@v/list"; then
		row modules ok "GOPROXY=$configured"
		# curl and Go verify TLS differently. Behind an intercepting proxy, or
		# in a macOS sandbox without the trust daemon, only Go fails.
		tls_probe="$(cd "$root" && { GOTOOLCHAIN=local GOFLAGS='' GOPROXY="$configured" "$go_bin" list -m -versions golang.org/x/mod 2>&1 || true; })"
		if [[ $tls_probe == *x509* || $tls_probe == *certificate* ]]; then
			need tls "Go cannot verify $first ($(grep -m1 -o 'x509:.*' <<<"$tls_probe")). Export SSL_CERT_FILE to your CA bundle (on macOS: export SSL_CERT_FILE=/etc/ssl/cert.pem) and rerun"
		fi
	else
		row modules offline "$first unreachable; builds use the module cache only (GOPROXY=direct fetches from each module's origin repository)"
	fi
fi

if ((!check)) && [[ -n $go_bin ]]; then
	vars=()
	[[ $go_mode == pinned ]] && vars+=("PATH=$pinned/bin:\$PATH" "GOTOOLCHAIN=local")
	vars+=("GOFLAGS=-modcacherw")
	{
		echo "# Written by automation/dev/setup.sh. Rerun it to change these values."
		echo "unset GOROOT GOBIN"
		for v in "${vars[@]}"; do echo "export ${v%%=*}=\"${v#*=}\""; done
	} >"$dev_home/env.sh"
	{
		echo "# Written by automation/dev/setup.sh. Rerun it to change these values."
		echo "# A GOROOT or GOBIN exported by another installer overrides the pinned Go."
		echo "unexport GOROOT GOBIN"
		for v in "${vars[@]}"; do
			value="${v#*=}"
			echo "export ${v%%=*} := ${value//\$PATH/\$(PATH)}"
		done
	} >"$dev_home/env.mk"
	row env ok "$dev_home/env.mk (make) and env.sh (shells)"
fi

if ((build && !check)) && [[ -n $go_bin ]]; then
	start=$SECONDS
	# shellcheck source=/dev/null # env.sh is written above for this machine.
	if (set -a && source "$dev_home/env.sh" && set +a && cd "$root" && "$go_bin" build -buildvcs=false -trimpath -o bin/wopr ./cmd/wopr); then
		row build ok "bin/wopr in $((SECONDS - start)) s ($("$root/bin/wopr" --version 2>/dev/null | head -1))"
	else
		need build "go build ./cmd/wopr failed; the error is above"
	fi
fi

echo
echo "WOPR development environment ($dev_home)"
printf '%s\n' "${summary[@]}"
if ((missing)); then
	echo "setup: FAILED"
	exit 1
fi
echo "setup: done. Next: make help"
