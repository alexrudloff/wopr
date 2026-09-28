#!/usr/bin/env bash
# automation/dev/login-copilot.sh: interactive helper for setting up a wopr
# auth.json so live tests have a credential to use.
#
# Live integration tests look for an OAuth credential at
# $WOPR_HOME/agent/auth.json (or ~/.wopr/agent/auth.json). This script
# runs the github-copilot device-code flow if no credential is
# present, or prints the existing credential's expiry if one exists.
#
# Usage:
#   automation/dev/login-copilot.sh           # login if missing, else show status
#   automation/dev/login-copilot.sh --force   # re-run device flow even if creds exist
#   automation/dev/login-copilot.sh --status  # only print status; never prompt
#
# After this, run:
#   go test -tags="integration live" ./tests/integration/...

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
WOPR_HOME="${WOPR_HOME:-$HOME/.wopr}"
AUTH_FILE="$WOPR_HOME/agent/auth.json"

show_status() {
  if [[ ! -f "$AUTH_FILE" ]]; then
    echo "❌ no auth.json at $AUTH_FILE"
    return 1
  fi
  if ! command -v jq >/dev/null 2>&1; then
    echo "✅ auth.json present at $AUTH_FILE (install jq for expiry detail)"
    return 0
  fi
  local count
  count="$(jq 'length' "$AUTH_FILE")"
  if [[ $count == 0 ]]; then
    echo "❌ $AUTH_FILE is empty"
    return 1
  fi
  echo "✅ $AUTH_FILE contains $count provider(s):"
  jq -r --argjson now "$(date +%s)" '
    to_entries[]
    | "   · \(.key)  type=\(.value.type // "?")"
      + (if (.value | has("expires")) then
           ((.value.expires / 1000 - $now) / 3600) as $hours
           | "  (\(if $hours > 0 then "valid" else "EXPIRED" end), \(if $hours >= 0 then "+" else "" end)\(($hours * 10 | round) / 10)h)"
         else "" end)' "$AUTH_FILE"
}

force=0
status_only=0
for arg in "$@"; do
  case "$arg" in
    --force) force=1 ;;
    --status) status_only=1 ;;
    -h|--help)
      sed -n '2,17p' "$0"
      exit 0
      ;;
    *)
      echo "unknown arg: $arg" >&2
      exit 2
      ;;
  esac
done

if [[ $status_only -eq 1 ]]; then
  show_status
  exit $?
fi

if [[ -f "$AUTH_FILE" && $force -eq 0 ]]; then
  show_status
  echo
  echo "Use --force to re-run the device flow."
  exit 0
fi

# Build wopr if needed.
BIN="$REPO_ROOT/bin/wopr"
mkdir -p "$REPO_ROOT/bin"
echo "Building wopr…"
(cd "$REPO_ROOT" && go build -o "$BIN" ./cmd/wopr)

echo
echo "Running github-copilot device-code flow."
echo "Follow the prompts (open the URL, paste the code in your browser)."
echo
"$BIN" login github-copilot

echo
show_status
