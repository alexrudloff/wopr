package serve

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
)

// TestKKCodeConnectSequenceReplaysTheCapturedAppFlow replays, in order, the
// request sequence the KKCode iOS client (UA KKCode/29694823) issued during a
// connect -> project -> session -> prompt flow, captured through a logging proxy
// against a real opencode 1.18.34 server. It asserts each step's status and the
// JSON kind of the fields the client dereferences, so a DTO that drops an
// object or array the app spreads (the "cannot convert undefined value to
// object" crash) fails here instead of in the app.
//
// The server-level failures this guards against are the ones actually observed:
// a 404 on the /api/health handshake, and a bare/misshaped DTO. It cannot catch
// UI-side rendering, which needs the real app.
//
// ponytail: the sequence is transcribed from the capture, not replayed live. If
// KKCode's flow changes, refresh it from a fresh proxy log.
func TestKKCodeConnectSequenceReplaysTheCapturedAppFlow(t *testing.T) {
	env := promptEnvFor(t)
	dir := url.QueryEscape(env.services.CWD())

	// Handshake. This 404 was what aborted the whole connect before the
	// compatibility wave shipped.
	status, body := getRaw(t, env.baseURL, "/api/health")
	if status != http.StatusOK {
		t.Fatalf("GET /api/health: got %d (%s)", status, body)
	}
	if v, ok := objKeys(t, body)["healthy"]; !ok || string(v) != "true" {
		t.Fatalf("GET /api/health: healthy = %s, want true", v)
	}

	// Health, session list, and the root file listing.
	for _, path := range []string{"/global/health", "/experimental/session", "/file?path="} {
		status, body = getRaw(t, env.baseURL, path)
		if status != http.StatusOK {
			t.Fatalf("GET %s: got %d (%s)", path, status, body)
		}
	}

	// GET /project: the app dereferences each project's time and sandboxes, so
	// both must be present with the right kind, not omitted.
	status, body = getRaw(t, env.baseURL, "/project")
	if status != http.StatusOK {
		t.Fatalf("GET /project: got %d (%s)", status, body)
	}
	var projects []map[string]json.RawMessage
	if err := json.Unmarshal(body, &projects); err != nil || len(projects) == 0 {
		t.Fatalf("GET /project: bad body (err=%v body=%s)", err, body)
	}
	assertKind(t, projects[0], "time", "object")
	assertKind(t, projects[0], "sandboxes", "array")

	// Create the session the app then reads around.
	status, body = postJSON(t, env.baseURL, "/session?directory="+dir, "{}")
	if status != http.StatusOK {
		t.Fatalf("POST /session: got %d (%s)", status, body)
	}
	var sess sessionDTO
	if err := json.Unmarshal(body, &sess); err != nil || sess.ID == "" {
		t.Fatalf("POST /session: bad id (err=%v body=%s)", err, body)
	}

	// The read fan the app fires once a session is open; every one answers an
	// array. A non-array body here is the "undefined to object" class again.
	for _, path := range []string{
		"/session/" + sess.ID + "/todo?directory=" + dir,
		"/session/" + sess.ID + "/message?limit=30&directory=" + dir,
		"/permission?directory=" + dir,
		"/question?directory=" + dir,
		"/agent?directory=" + dir,
	} {
		status, body = getRaw(t, env.baseURL, path)
		if status != http.StatusOK {
			t.Fatalf("GET %s: got %d (%s)", path, status, body)
		}
		var arr []json.RawMessage
		if err := json.Unmarshal(body, &arr); err != nil {
			t.Fatalf("GET %s: not a JSON array: %v (%s)", path, err, body)
		}
	}

	// /api/command: {location, data[]}.
	status, body = getRaw(t, env.baseURL, "/api/command?location%5Bdirectory%5D="+dir)
	if status != http.StatusOK {
		t.Fatalf("GET /api/command: got %d (%s)", status, body)
	}
	cmd := objKeys(t, body)
	assertKind(t, cmd, "location", "object")
	assertKind(t, cmd, "data", "array")

	// /config/providers: {providers[], default{}} -- the object the app spreads.
	status, body = getRaw(t, env.baseURL, "/config/providers?directory="+dir)
	if status != http.StatusOK {
		t.Fatalf("GET /config/providers: got %d (%s)", status, body)
	}
	pro := objKeys(t, body)
	assertKind(t, pro, "providers", "array")
	assertKind(t, pro, "default", "object")

	// /config: the 12-key object the app iterates; object/array kinds matter.
	status, body = getRaw(t, env.baseURL, "/config?directory="+dir)
	if status != http.StatusOK {
		t.Fatalf("GET /config: got %d (%s)", status, body)
	}
	cfg := objKeys(t, body)
	for _, key := range []string{"agent", "command", "mcp", "mode", "permission", "provider"} {
		assertKind(t, cfg, key, "object")
	}
	for _, key := range []string{"instructions", "plugin"} {
		assertKind(t, cfg, key, "array")
	}
	// The app reads individual permission entries; an absent key is undefined.
	perm := assertObject(t, cfg, "permission")
	assertKind(t, perm, "bash", "string")
	assertKind(t, perm, "external_directory", "object")

	// The accepted prompt. 204 is the app's cue to stream.
	status, body = postJSON(t, env.baseURL,
		"/session/"+sess.ID+"/prompt_async?directory="+dir,
		`{"parts":[{"type":"text","text":"ping"}]}`)
	if status != http.StatusNoContent {
		t.Fatalf("POST prompt_async: got %d (%s), want 204", status, body)
	}
}
