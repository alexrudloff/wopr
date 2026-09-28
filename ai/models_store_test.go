package ai

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestFileModelsStoreCreatesFilePreservesOrderAndRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent", "models-store.json")
	store := NewFileModelsStore(path)
	ctx := context.Background()

	entry, err := store.Read(ctx, "llama.cpp")
	if err != nil || entry != nil {
		t.Fatalf("Read(empty) = %v, %v; want nil, nil", entry, err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "{}" {
		t.Fatalf("store file after first read = %q, %v; want {}", data, err)
	}
	// Windows file modes carry no group/other bits (Go reports 0666 or
	// 0444, as Node ignores the mode there), so owner-only is checked on Unix.
	info, err := os.Stat(path)
	if err != nil || runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("store mode = %v, %v; want 0600", info.Mode().Perm(), err)
	}

	if err := os.WriteFile(path, []byte(`{"zeta":{"models":[]},"alpha":{"models":[{"id":"a<b"}],"etag":"\"x\""}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	checkedAt := 1700000000000.0
	if err := store.Write(ctx, "llama.cpp", ModelsStoreEntry{Models: []json.RawMessage{json.RawMessage(`{"id":"m"}`)}, CheckedAt: &checkedAt}); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := `{
  "zeta": {
    "models": []
  },
  "alpha": {
    "models": [
      {
        "id": "a<b"
      }
    ],
    "etag": "\"x\""
  },
  "llama.cpp": {
    "models": [
      {
        "id": "m"
      }
    ],
    "checkedAt": 1700000000000
  }
}`
	if string(data) != want {
		t.Fatalf("store file =\n%s\nwant\n%s", data, want)
	}
	entry, err = store.Read(ctx, "llama.cpp")
	if err != nil || entry == nil || len(entry.Models) != 1 || entry.CheckedAt == nil || *entry.CheckedAt != checkedAt {
		t.Fatalf("Read(llama.cpp) = %+v, %v", entry, err)
	}
	alpha, err := store.Read(ctx, "alpha")
	if err != nil || alpha == nil || alpha.ETag != `"x"` {
		t.Fatalf("Read(alpha) = %+v, %v", alpha, err)
	}
}

// FileModelsStore.delete rewrites JSON.stringify(current, null, 2) without the
// provider and keeps the remaining keys in order.
func TestFileModelsStoreDeleteKeepsOtherProvidersInOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models-store.json")
	if err := os.WriteFile(path, []byte(`{"zeta":{"models":[]},"example":{"models":[{"id":"auto"}],"checkedAt":1},"alpha":{"models":[]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	store := NewFileModelsStore(path)
	ctx := context.Background()
	if err := store.Delete(ctx, "example"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"zeta\": {\n    \"models\": []\n  },\n  \"alpha\": {\n    \"models\": []\n  }\n}"
	if string(data) != want {
		t.Fatalf("store file =\n%s\nwant\n%s", data, want)
	}
	if entry, err := store.Read(ctx, "example"); err != nil || entry != nil {
		t.Fatalf("Read(deleted) = %+v, %v; want nil, nil", entry, err)
	}
	if err := store.Delete(ctx, "zeta"); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(ctx, "alpha"); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "{}" {
		t.Fatalf("store file after deleting every provider = %q, %v; want {}", data, err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := store.Delete(cancelled, "x"); err == nil {
		t.Fatal("Delete with a cancelled context succeeded")
	}
}
