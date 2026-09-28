#!/usr/bin/env bash
# automation/ci/integration-tests.sh: split the integration suite by category so
# the long-running LLM-driven tests don't gate every commit.
#
# Categories (from AGENTS.md "Functional tests over unit tests"):
#
#   fast      : no LLM calls, deterministic. Runs in ~30s. Gate every commit.
#   live      : live model tests (TestLive_*). Uses real provider config. Run manually before a
#               release; never part of `make check`.
#   all       : every integration test, live included.
#
# Usage: ./automation/ci/integration-tests.sh [fast|live|all] [-- extra go test flags...]
#
# Hard rules (per AGENTS.md):
#   - Never use `tmux kill-server`. Each test creates its own session by
#     PID-randomized name and kills only itself.
#   - The live tier runs against WOPR_LIVE_PROVIDER/WOPR_LIVE_MODEL, a
#     configured provider/model; its tests skip when either is unset.

set -euo pipefail

cd "$(dirname "$0")/../.."

CATEGORY="${1:-fast}"
shift || true

TAGS=integration
export WOPR_LIVE_PROVIDER="${WOPR_LIVE_PROVIDER:-}"
export WOPR_LIVE_MODEL="${WOPR_LIVE_MODEL:-}"

# Patterns matched against `go test -run`. Categories are mutually exclusive
# except for "all".
case "$CATEGORY" in
    fast)
        # No LLM (faux provider, no network): tmux start/slash/Shift+Enter,
        # prompt+tool+exit, SIGINT, JSON streaming/cancel, session dir+resume.
        PATTERN='^(TestSlash|TestShiftEnter|TestExitClean|TestInteractiveSigint|TestJSONMode|TestSessionDir)'
        TIMEOUT=300s
        ;;
    live)
        # Live model tests against WOPR_LIVE_PROVIDER/WOPR_LIVE_MODEL.
        PATTERN='^TestLive_'
        TAGS='integration live'
        TIMEOUT=1800s
        ;;
    all)
        PATTERN='.'
        TAGS='integration live'
        TIMEOUT=2400s
        ;;
    *)
        echo "usage: $0 [fast|live|all] [-- extra go test args]" >&2
        exit 2
        ;;
esac

echo "== integration tests: category=$CATEGORY pattern=$PATTERN timeout=$TIMEOUT"
echo

exec go test -tags "$TAGS" -count=1 -timeout "$TIMEOUT" -v \
    -run "$PATTERN" \
    ./tests/integration/... "$@"
