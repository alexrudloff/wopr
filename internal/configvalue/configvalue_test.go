package configvalue

import (
	"context"
	"sync/atomic"
	"testing"
)

func TestResolveDollarEnvExpands(t *testing.T) {
	ClearCache()
	t.Setenv("WOPR_TEST_VAR", "abc-123")
	for _, in := range []string{"$WOPR_TEST_VAR", "${WOPR_TEST_VAR}"} {
		if got := Resolve(in, nil); got != "abc-123" {
			t.Fatalf("Resolve(%q, nil) = %q, want abc-123", in, got)
		}
	}
}

func TestResolveBangCmdRunsShell(t *testing.T) {
	ClearCache()
	if got := Resolve("!echo hello", nil); got != "hello" {
		t.Fatalf("expected hello, got %q", got)
	}
}

func TestResolveBangCmdCached(t *testing.T) {
	ClearCache()
	var calls atomic.Int32
	restore := SetExecutorForTest(func(_ context.Context, payload string) (string, bool) {
		calls.Add(1)
		if payload != "echo cached" {
			t.Fatalf("unexpected payload %q", payload)
		}
		return "cached-result", true
	})
	defer restore()

	for i := range 3 {
		if got := Resolve("!echo cached", nil); got != "cached-result" {
			t.Fatalf("call %d: got %q", i, got)
		}
	}
	if c := calls.Load(); c != 1 {
		t.Fatalf("expected 1 shell-out, got %d", c)
	}
}

func TestResolveOrErrorBangFailure(t *testing.T) {
	ClearCache()
	if _, err := ResolveOrError("!false", "test key", nil); err == nil {
		t.Fatalf("expected error for failing !cmd")
	}
}

// TestResolve_ProviderScopedEnvPrecedence verifies that provider-scoped env
// overrides take precedence over the process environment when resolving a
// `$VAR` reference, fall back to the process env when absent from the scope,
// and that nil scope preserves process-env behavior.
func TestResolve_ProviderScopedEnvPrecedence(t *testing.T) {
	t.Setenv("WOPR_CV_SCOPED", "from-process")
	t.Setenv("WOPR_CV_ONLYPROC", "proc-only")

	scope := map[string]string{"WOPR_CV_SCOPED": "from-scope"}

	// scoped value wins over process env
	if got := Resolve("$WOPR_CV_SCOPED", scope); got != "from-scope" {
		t.Errorf("scoped env should win: got %q want %q", got, "from-scope")
	}
	// nil scope falls back to process env
	if got := Resolve("$WOPR_CV_SCOPED", nil); got != "from-process" {
		t.Errorf("nil scope should use process env: got %q want %q", got, "from-process")
	}
	// var absent from scope falls back to process env
	if got := Resolve("$WOPR_CV_ONLYPROC", scope); got != "proc-only" {
		t.Errorf("missing-from-scope should fall back to process env: got %q want %q", got, "proc-only")
	}
	// empty scoped value is treated as unset and falls back to process env
	if got := Resolve("$WOPR_CV_SCOPED", map[string]string{"WOPR_CV_SCOPED": ""}); got != "from-process" {
		t.Errorf("empty scoped value should fall back to process env: got %q want %q", got, "from-process")
	}
}
