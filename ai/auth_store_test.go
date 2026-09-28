package ai

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/alexrudloff/wopr/internal/configvalue"
)

func TestReadOnlyAuthStorageDoesNotCreateFilesOrRunCommands(t *testing.T) {
	executed := false
	restore := configvalue.SetExecutorForTest(func(context.Context, string) (string, bool) {
		executed = true
		return "command-output", true
	})
	t.Cleanup(restore)
	configvalue.ClearCache()
	t.Setenv("WOPR_TEST_READONLY_KEY", "env-key")

	dir := t.TempDir()
	missing := filepath.Join(dir, "agent", "auth.json")
	store := NewReadOnlyAuthStorage(missing)
	credential, err := store.Read(context.Background(), "openai")
	if err != nil || credential != nil {
		t.Fatalf("Read(missing) = %v, %v; want nil, nil", credential, err)
	}
	if infos, err := store.List(context.Background()); err != nil || len(infos) != 0 {
		t.Fatalf("List(missing) = %v, %v", infos, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "agent")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only store created its directory: %v", err)
	}

	path := filepath.Join(dir, "auth.json")
	writeAuthFixture(t, path, `{"cmd":{"type":"api_key","key":"!op read secret"},"env":{"type":"api_key","key":"$WOPR_TEST_READONLY_KEY"},"codex":{"type":"oauth","access":"a","refresh":"r","expires":1}}`)
	store = NewReadOnlyAuthStorage(path)
	command, err := store.Read(context.Background(), "cmd")
	if err != nil || command.Key != "!op read secret" {
		t.Fatalf("Read(cmd) = %+v, %v; want unresolved command key", command, err)
	}
	if executed {
		t.Fatal("read-only store executed a configured key command")
	}
	resolved, err := store.Read(context.Background(), "env")
	if err != nil || resolved.Key != "env-key" {
		t.Fatalf("Read(env) = %+v, %v; want resolved env reference", resolved, err)
	}
	infos, err := store.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []CredentialInfo{{"cmd", CredentialAPIKey}, {"codex", CredentialOAuth}, {"env", CredentialAPIKey}}
	if len(infos) != len(want) {
		t.Fatalf("List = %+v", infos)
	}
	for index := range want {
		if infos[index] != want[index] {
			t.Fatalf("List = %+v, want %+v", infos, want)
		}
	}
	if _, err := store.Modify(context.Background(), "cmd", func(*Credential) (*Credential, error) { return nil, nil }); err == nil ||
		err.Error() != "Read-only credential storage cannot modify auth.json" {
		t.Fatalf("Modify error = %v", err)
	}
}

func TestAuthStorageModifyIsTheCredentialWritePath(t *testing.T) {
	t.Setenv("WOPR_TEST_STORE_KEY", "resolved-key")
	path := filepath.Join(t.TempDir(), "auth.json")
	store, err := NewAuthStorage(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Set("openai", Credential{Type: CredentialAPIKey, Key: "$WOPR_TEST_STORE_KEY"}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	read, err := store.Read(ctx, "openai")
	if err != nil || read.Key != "resolved-key" {
		t.Fatalf("Read = %+v, %v; want resolved key", read, err)
	}
	unchanged, err := store.Modify(ctx, "openai", func(current *Credential) (*Credential, error) {
		if current == nil || current.Key != "$WOPR_TEST_STORE_KEY" {
			t.Fatalf("Modify saw %+v; want the raw stored credential", current)
		}
		return nil, nil
	})
	if err != nil || unchanged.Key != "$WOPR_TEST_STORE_KEY" {
		t.Fatalf("Modify(nil) = %+v, %v; want the current credential", unchanged, err)
	}
	written, err := store.Modify(ctx, "codex", func(current *Credential) (*Credential, error) {
		if current != nil {
			t.Fatalf("Modify saw %+v for a missing provider", current)
		}
		return &Credential{Type: CredentialOAuth, Access: "fresh", Refresh: "r", Expires: 42}, nil
	})
	if err != nil || written.Access != "fresh" {
		t.Fatalf("Modify(write) = %+v, %v", written, err)
	}
	raw, ok, err := store.GetRaw("codex")
	if err != nil || !ok || raw.Access != "fresh" || raw.Expires != 42 {
		t.Fatalf("persisted = %+v, %v, %v", raw, ok, err)
	}
	failure := errors.New("refresh failed")
	if _, err := store.Modify(ctx, "codex", func(*Credential) (*Credential, error) { return nil, failure }); !errors.Is(err, failure) {
		t.Fatalf("Modify error = %v; want the callback error", err)
	}
	infos, err := store.List(ctx)
	if err != nil || len(infos) != 2 || infos[0].ProviderID != "codex" || infos[1].ProviderID != "openai" {
		t.Fatalf("List = %+v, %v", infos, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := store.Read(canceled, "openai"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Read(canceled) = %v", err)
	}
}

func writeAuthFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
