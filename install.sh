#!/bin/sh
# WOPR installer for macOS and Linux.
#
#   curl -fsSL https://raw.githubusercontent.com/alexrudloff/wopr/main/install.sh | sh
#
# Downloads the WOPR release archive for this machine from GitHub Releases,
# verifies it against the release's SHA256SUMS, and installs the wopr binary.
# It fails closed: no published release, a missing checksum, or a checksum
# mismatch stops the install before anything is written to the install
# directory. This script checks SHA-256 only; `wopr update` additionally
# verifies the Ed25519 signature on SHA256SUMS (SHA256SUMS.sig) before it
# replaces the binary.
#
# Environment:
#   WOPR_VERSION        install this version (for example 0.2.0) instead of the latest
#   WOPR_INSTALL_DIR    install directory (default: $HOME/.local/bin)
#   WOPR_RELEASES       releases URL (default: https://github.com/alexrudloff/wopr/releases)
#
# The whole script is one function called on its last line, so a truncated
# download runs nothing.

set -eu

main() {
  releases=${WOPR_RELEASES:-https://github.com/alexrudloff/wopr/releases}
  releases=${releases%/}
  install_dir=${WOPR_INSTALL_DIR:-${HOME:?HOME is not set}/.local/bin}

  need uname
  need tar
  need mktemp
  downloader=$(pick_downloader)
  platform=$(detect_platform)

  version=${WOPR_VERSION:-}
  if [ -z "$version" ]; then
    version=$(latest_version "$downloader" "$releases")
  fi
  version=${version#v}
  valid_version "$version" || fail "not a release version: $version"

  name="wopr-${version}-${platform}"
  archive="${name}.tar.gz"
  release_url="${releases}/download/v${version}"

  work=$(mktemp -d 2>/dev/null || mktemp -d -t wopr-install)
  trap 'rm -rf "$work"' EXIT
  trap 'exit 130' INT
  trap 'exit 143' TERM

  say "Downloading WOPR ${version} for ${platform}"
  fetch "$downloader" "${release_url}/SHA256SUMS" "$work/SHA256SUMS" ||
    fail "WOPR ${version} has no SHA256SUMS at ${release_url}; refusing to install an unverified archive"
  fetch "$downloader" "${release_url}/${archive}" "$work/$archive" ||
    fail "could not download ${release_url}/${archive}"

  expected=$(expected_sha256 "$work/SHA256SUMS" "$archive")
  [ -n "$expected" ] || fail "SHA256SUMS has no single valid entry for ${archive}"
  actual=$(sha256_of "$work/$archive")
  [ "$actual" = "$expected" ] || fail "checksum mismatch for ${archive}: expected ${expected}, got ${actual}"
  say "Verified SHA-256 ${actual}"

  mkdir "$work/extract"
  tar -xzf "$work/$archive" -C "$work/extract"
  binary="$work/extract/${name}/wopr"
  if [ ! -f "$binary" ] || [ -L "$binary" ]; then
    fail "${archive} does not contain ${name}/wopr"
  fi

  # Stage beside the destination (a noexec /tmp cannot run the smoke test),
  # then replace any existing wopr in one rename.
  mkdir -p "$install_dir"
  staged="${install_dir}/.wopr.install.$$"
  trap 'rm -rf "$work" "$staged"' EXIT
  cp "$binary" "$staged"
  chmod 0755 "$staged"
  "$staged" --version >/dev/null 2>&1 || fail "the downloaded wopr binary does not run on this machine"
  mv -f "$staged" "${install_dir}/wopr"

  say "Installed $("${install_dir}/wopr" --version 2>/dev/null || printf 'wopr %s' "$version") to ${install_dir}/wopr"
  case ":${PATH:-}:" in
    *":${install_dir}:"*) ;;
    *) say "Add ${install_dir} to PATH to run wopr, for example: export PATH=\"${install_dir}:\$PATH\"" ;;
  esac
}

say() {
  printf 'wopr-install: %s\n' "$*"
}

fail() {
  printf 'wopr-install: error: %s\n' "$*" >&2
  exit 1
}

need() {
  command -v "$1" >/dev/null 2>&1 || fail "this installer needs '$1'"
}

pick_downloader() {
  if command -v curl >/dev/null 2>&1; then
    echo curl
  elif command -v wget >/dev/null 2>&1; then
    echo wget
  else
    fail "this installer needs curl or wget"
  fi
}

# fetch DOWNLOADER URL FILE: HTTPS only, failing on any HTTP error.
fetch() {
  case "$1" in
    curl) curl --proto '=https' --tlsv1.2 -fsSL --retry 3 -o "$3" "$2" ;;
    wget) wget --https-only -q -O "$3" "$2" ;;
  esac
}

detect_platform() {
  case "$(uname -s)" in
    Linux) os=linux ;;
    Darwin) os=darwin ;;
    *) fail "unsupported operating system $(uname -s); download a release archive from GitHub instead" ;;
  esac
  case "$(uname -m)" in
    x86_64 | amd64) arch=amd64 ;;
    arm64 | aarch64) arch=arm64 ;;
    *) fail "unsupported CPU architecture $(uname -m)" ;;
  esac
  # A Rosetta 2 shell on Apple silicon reports x86_64; install the native binary.
  if [ "$os" = darwin ] && [ "$arch" = amd64 ] &&
    [ "$(sysctl -n sysctl.proc_translated 2>/dev/null || echo 0)" = 1 ]; then
    arch=arm64
  fi
  echo "${os}-${arch}"
}

# latest_version DOWNLOADER RELEASES reads the tag that RELEASES/latest
# redirects to (.../releases/tag/vX.Y.Z), which needs no GitHub API quota.
latest_version() {
  location=$(latest_location "$1" "${2}/latest") || location=""
  tag=${location##*/releases/tag/}
  if [ -z "$location" ] || [ "$tag" = "$location" ] || [ -z "$tag" ]; then
    fail "no WOPR release is published yet (${2}/latest does not name a release); build from source or set WOPR_VERSION"
  fi
  echo "${tag#v}"
}

# latest_location DOWNLOADER URL prints the redirect target of URL without
# following it.
latest_location() {
  case "$1" in
    curl) curl --proto '=https' --tlsv1.2 -fsS --retry 3 -o /dev/null -w '%{redirect_url}' "$2" ;;
    wget) { wget --https-only --max-redirect=0 -S -O /dev/null "$2" 2>&1 || true; } |
      awk 'tolower($1) == "location:" { print $2 }' | tr -d '\r' | tail -n 1 ;;
  esac
}

# valid_version VERSION: SemVer core with optional pre-release and build parts.
valid_version() {
  printf '%s\n' "$1" | grep -Eq '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$'
}

# expected_sha256 SUMS NAME prints the digest of NAME (listed as NAME or ./NAME)
# only when exactly one well-formed line names it.
expected_sha256() {
  awk -v name="$2" '
    ($2 == name || $2 == "./" name || $2 == "*" name || $2 == "*./" name) && NF == 2 {
      count++
      digest = tolower($1)
    }
    END {
      if (count == 1 && digest ~ /^[0-9a-f]+$/ && length(digest) == 64) print digest
    }
  ' "$1"
}

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print tolower($1)}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print tolower($1)}'
  elif command -v openssl >/dev/null 2>&1; then
    openssl dgst -sha256 -r "$1" | awk '{print tolower($1)}'
  else
    fail "this installer needs sha256sum, shasum, or openssl to verify the download"
  fi
}

main "$@"
