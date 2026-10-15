package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
	codinghost "github.com/alexrudloff/wopr/coding/host"
	"github.com/alexrudloff/wopr/coding/version"
	icodingagent "github.com/alexrudloff/wopr/internal/codingagent"
	"github.com/alexrudloff/wopr/internal/codingagent/askuser"
	"github.com/alexrudloff/wopr/internal/codingagent/tools"
)

// ─── Shared ──────────────────────────────────────────────────────────────

const (
	defaultLimit = 50
	maxLimit     = 200
)

// writeJSON writes v as JSON with the given status. Sets Content-Type and
// disables HTML escaping so that session titles, message text and file names
// containing &, <, > are not mangled.
func (s *Server) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

// limitOf parses the "limit" query parameter. Non-integer or negative → def.
// Above max → clamped to max.
func limitOf(r *http.Request, def, max int) int {
	q := r.URL.Query().Get("limit")
	if q == "" {
		return def
	}
	v, err := strconv.Atoi(q)
	if err != nil || v < 0 {
		return def
	}
	if v > max {
		return max
	}
	return v
}

// directoryParam validates the "directory" query parameter against the
// server's resolved root. If present and different → 400 error.
func (s *Server) directoryParam(r *http.Request) (string, *errorDTO, int) {
	q := r.URL.Query().Get("directory")
	if q == "" {
		return s.sessionDir, nil, 0
	}
	resolvedRoot, _ := filepath.EvalSymlinks(s.services.CWD())
	qResolved, _ := filepath.EvalSymlinks(q)
	if qResolved != resolvedRoot {
		return "", &errorDTO{Error: "directory parameter must match server root"}, http.StatusBadRequest
	}
	return s.sessionDir, nil, 0
}

// notImplemented is the shared stub for the four 501 routes.
func (s *Server) notImplemented(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusNotImplemented, errorDTO{Error: "not implemented"})
}

// ─── Handlers ────────────────────────────────────────────────────────────

// GET /global/health
func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, healthResponse{
		Healthy: true,
		Version: version.Version,
	})
}

// GET /api/health
func (s *Server) apiHealth(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, apiHealthResponse{Healthy: true})
}

// GET /config
func (s *Server) getConfig(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, configDTO{
		Command:      map[string]any{},
		Plugin:       []any{},
		Model:        s.modelSpec,
		Mode:         map[string]any{},
		Agent:        map[string]any{},
		Provider:     map[string]any{},
		MCP:          map[string]any{},
		Instructions: []any{},
		// wopr has no approval gate, so every action is allowed. The keys mirror
		// opencode's so a client that reads an individual entry finds it
		// (reading a missing permission key is the same crash class as an
		// undefined object).
		Permission: map[string]any{
			"read": "allow", "edit": "allow", "glob": "allow", "grep": "allow",
			"list": "allow", "bash": "allow", "task": "allow", "todowrite": "allow",
			"question": "allow", "webfetch": "allow", "websearch": "allow",
			"lsp": "allow", "skill": "allow",
			"external_directory": map[string]any{"*": "allow"},
		},
	})
}

// GET /api/command
func (s *Server) apiCommand(w http.ResponseWriter, r *http.Request) {
	dir, _ := filepath.EvalSymlinks(s.services.CWD())
	s.writeJSON(w, http.StatusOK, apiCommandDTO{
		Location: commandLocationDTO{
			Directory: dir,
			Project:   commandLocationProjectDTO{ID: "global", Directory: dir},
		},
		Data: []any{},
	})
}

// GET /permission
//
// Every tool is open by default, so there are never pending requests; the
// client polls this and reads a non-array body as a broken host.
func (s *Server) listPermissions(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, []any{})
}

// GET /session — inclusive; archived sessions are listed.
func (s *Server) listSessions(w http.ResponseWriter, r *http.Request) {
	s.listSessionsFiltered(w, r, true)
}

// GET /experimental/session — opencode's route excludes archived sessions.
func (s *Server) listExperimentalSessions(w http.ResponseWriter, r *http.Request) {
	s.listSessionsFiltered(w, r, false)
}

func (s *Server) listSessionsFiltered(w http.ResponseWriter, r *http.Request, includeArchived bool) {
	limit := limitOf(r, defaultLimit, maxLimit)
	dir, errDTO, status := s.directoryParam(r)
	if errDTO != nil {
		s.writeJSON(w, status, errDTO)
		return
	}

	// Disk sessions.
	entries, err := os.ReadDir(dir)
	if err != nil {
		s.writeJSON(w, http.StatusOK, []sessionDTO{})
		return
	}

	type diskRec struct {
		rec sessionRecord
		err error
	}
	// Cap concurrent goroutines to maxListFanOut (MAJOR 4).
	sem := make(chan struct{}, maxListFanOut)
	diskCh := make(chan diskRec, len(entries))
	var wg sync.WaitGroup
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(name string) {
			defer func() {
				wg.Done()
				<-sem
			}()
			path := filepath.Join(dir, name)
			rec, ok := sessionRecordAt(path)
			if !ok {
				diskCh <- diskRec{err: fmt.Errorf("skip")}
				return
			}
			diskCh <- diskRec{rec: rec, err: nil}
		}(entry.Name())
	}

	// Close channel after all workers finish.
	go func() {
		wg.Wait()
		close(diskCh)
	}()

	diskMap := make(map[string]sessionRecord)
	for dr := range diskCh {
		if dr.err == nil {
			diskMap[dr.rec.ID] = dr.rec
		}
	}

	// Live sessions from host registry.
	liveSessions := s.host.Sessions()
	for _, ls := range liveSessions {
		id := ls.ID()
		if _, exists := diskMap[id]; exists {
			continue
		}
		rec, _ := getSessionRecord(dir, id, ls)
		diskMap[id] = rec
	}

	// Convert to slice and sort by (mtime desc, header timestamp desc, path asc).
	type sortable struct {
		rec sessionRecord
		key string
	}
	var sortableRecs []sortable
	for _, rec := range diskMap {
		sortableRecs = append(sortableRecs, sortable{
			rec: rec,
			key: fmt.Sprintf("%s|%s|%s",
				rec.ModTime.Format(time.RFC3339Nano),
				rec.Header.Timestamp,
				rec.Path,
			),
		})
	}
	slices.SortFunc(sortableRecs, func(a, b sortable) int {
		// mtime desc, timestamp desc, path asc
		if a.rec.ModTime.After(b.rec.ModTime) {
			return -1
		}
		if a.rec.ModTime.Before(b.rec.ModTime) {
			return 1
		}
		if a.rec.Header.Timestamp > b.rec.Header.Timestamp {
			return -1
		}
		if a.rec.Header.Timestamp < b.rec.Header.Timestamp {
			return 1
		}
		return strings.Compare(a.rec.Path, b.rec.Path)
	})

	// Build DTOs.
	dtos := make([]sessionDTO, 0)
	for _, sr := range sortableRecs {
		if !includeArchived && sr.rec.Archived != nil {
			continue
		}
		if len(dtos) >= limit {
			break
		}
		live, _ := s.host.Get(sr.rec.ID)
		dtos = append(dtos, s.buildSessionDTO(sr.rec, live))
	}

	s.writeJSON(w, http.StatusOK, dtos)
}

// POST /session
func (s *Server) createSession(w http.ResponseWriter, r *http.Request) {
	sess, err := s.host.Start(codinghost.StartOptions{
		Model:      s.modelSpec,
		SessionDir: s.sessionDir,
	})
	if err != nil {
		if errors.Is(err, codinghost.ErrNoModel) {
			s.writeJSON(w, http.StatusConflict, errorDTO{
				Error: "no model is configured for this server; start it with `wopr serve` after setting a default model (`wopr --model <provider/model> <prompt>`, or `settings.defaultModel`)",
			})
			return
		}
		s.writeJSON(w, http.StatusInternalServerError, errorDTO{Error: err.Error()})
		return
	}
	rec, _ := getSessionRecord(s.sessionDir, sess.ID(), sess)
	// The asker is what offers ask_user at all: a session joins the tool only
	// while one is installed, which is why print and RPC modes never show it.
	sess.Inner().SetUserAsker(func(ctx context.Context, q askuser.Question) (askuser.Answer, error) {
		return s.ask(ctx, sess.ID(), q)
	})
	s.writeJSON(w, http.StatusOK, s.buildSessionDTO(rec, sess))
}

// GET /session/{id}
func (s *Server) getSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	live, _ := s.host.Get(id)
	rec, ok := getSessionRecord(s.sessionDir, id, live)
	if !ok {
		s.writeJSON(w, http.StatusNotFound, errorDTO{Error: "session not found"})
		return
	}
	s.writeJSON(w, http.StatusOK, s.buildSessionDTO(rec, live))
}

// patchSessionBody is the request body for PATCH /session/{id}.
// Exported so TestRequestBodyFieldsAreBuiltins can reflect it (MAJOR 6).
type patchSessionBody struct {
	Title *string `json:"title"`
	Time  *struct {
		Archived *int64 `json:"archived"`
	} `json:"time"`
}

// PATCH /session/{id}
func (s *Server) patchSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var body patchSessionBody
	if err := json.NewDecoder(io.LimitReader(r.Body, maxBodyBytes)).Decode(&body); err != nil {
		s.writeJSON(w, http.StatusBadRequest, errorDTO{Error: "invalid body"})
		return
	}

	hasTitle := body.Title != nil && strings.TrimSpace(*body.Title) != ""
	hasTime := body.Time != nil

	if (hasTitle && hasTime) || (!hasTitle && !hasTime) {
		s.writeJSON(w, http.StatusBadRequest, errorDTO{Error: "exactly one of title or time.archived must be set"})
		return
	}

	live, _ := s.host.Get(id)
	rec, ok := getSessionRecord(s.sessionDir, id, live)
	if !ok {
		s.writeJSON(w, http.StatusNotFound, errorDTO{Error: "session not found"})
		return
	}

	sm := icodingagent.NewSessionManagerWithDir(s.services.CWD(), s.sessionDir)

	if hasTitle {
		title := strings.TrimSpace(*body.Title)
		if title == "" {
			s.writeJSON(w, http.StatusBadRequest, errorDTO{Error: "title must not be empty"})
			return
		}
		var err error
		if live != nil {
			err = live.Inner().SetSessionName(title)
		} else {
			err = sm.RenameSession(rec.Path, title)
		}
		if err != nil {
			s.writeJSON(w, http.StatusInternalServerError, errorDTO{Error: err.Error()})
			return
		}
	}

	if hasTime {
		if err := s.writeArchive(rec, live, body.Time.Archived); err != nil {
			s.writeJSON(w, http.StatusInternalServerError, errorDTO{Error: err.Error()})
			return
		}
	}

	// Re-read and return updated record.
	newRec, _ := getSessionRecord(s.sessionDir, id, live)
	info := s.buildSessionDTO(newRec, live)
	// The PATCH's whole result is this session's info, and a connected client
	// learns about a rename or an archive from the stream. The mutation is
	// already durable at this point, so a marshal failure is a mapper bug and
	// must not turn a successful 200 into an error.
	_ = s.host.Publish(codinghost.Event{
		Type:    eventSessionUpdated,
		Session: id,
		Data:    sseSessionUpdated{SessionID: id, Info: info},
	})
	s.writeJSON(w, http.StatusOK, info)
}

// writeArchive is the single writer of the session_archive metadata:
// PATCH {time:{archived}} and DELETE /session/{id} both route through it, so the
// record shape cannot drift. archived is nil to clear the stamp (PATCH only).
func (s *Server) writeArchive(rec sessionRecord, live *codinghost.Session, archived *int64) error {
	var data map[string]any
	if archived != nil {
		data = map[string]any{"archived": *archived}
	} else {
		data = map[string]any{"archived": nil}
	}
	if live != nil {
		return live.Inner().Inner().AppendCustomEntry("session_archive", data)
	}
	sm := icodingagent.NewSessionManagerWithDir(s.services.CWD(), s.sessionDir)
	return sm.AppendCustomEntry(rec.Path, "session_archive", data)
}

// DELETE /session/{id}
//
// wopr has no host delete primitive and never removes transcript files, so a
// delete archives: it stamps session_archive exactly as PATCH
// {time:{archived:<ms>}} does, then answers true. A deliberate divergence from
// opencode, whose DELETE hard-deletes. 404 for an unknown id.
func (s *Server) deleteSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	live, _ := s.host.Get(id)
	rec, ok := getSessionRecord(s.sessionDir, id, live)
	if !ok {
		s.writeJSON(w, http.StatusNotFound, errorDTO{Error: "session not found"})
		return
	}
	now := time.Now().UnixMilli()
	if err := s.writeArchive(rec, live, &now); err != nil {
		s.writeJSON(w, http.StatusInternalServerError, errorDTO{Error: err.Error()})
		return
	}
	s.writeJSON(w, http.StatusOK, true)
}

// POST /session/{id}/fork, POST /session/{id}/revert, GET /session/{id}/diff
// All share the same notImplemented stub.

// project builds the served-root project DTO. time and sandboxes are always
// present (see projectDTO); vcs is set only for a git worktree, matching how
// opencode annotates repositories.
func (s *Server) project() projectDTO {
	worktree, _ := filepath.EvalSymlinks(s.services.CWD())
	var mtime int64
	if info, err := os.Stat(worktree); err == nil {
		mtime = info.ModTime().UnixMilli()
	}
	dto := projectDTO{
		ID:        projectID(worktree),
		Worktree:  worktree,
		Time:      projectTime{Created: mtime, Updated: mtime},
		Sandboxes: []string{},
	}
	if _, err := os.Stat(filepath.Join(worktree, ".git")); err == nil {
		dto.VCS = "git"
	}
	return dto
}

// GET /project
func (s *Server) listProjects(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, []projectDTO{s.project()})
}

// GET /project/current
func (s *Server) getCurrentProject(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, s.project())
}

// GET /config/providers
func (s *Server) listProviders(w http.ResponseWriter, r *http.Request) {
	liveSessions := s.host.Sessions()
	var current *ai.Model
	if len(liveSessions) > 0 {
		current = liveSessions[0].Inner().Model()
	}
	models, err := codinghost.AvailableModels(s.services, current)
	if err != nil {
		s.writeJSON(w, http.StatusInternalServerError, errorDTO{Error: err.Error()})
		return
	}

	byProvider := make(map[string]providerDTO)
	for _, m := range models {
		pid := codinghost.ModelProviderID(m)
		pdto, ok := byProvider[pid]
		if !ok {
			pdto = providerDTO{
				ID:      pid,
				Name:    pid,
				Source:  "api",
				Env:     []string{},
				Options: map[string]any{},
				Models:  map[string]providerModelDTO{},
			}
		}
		pdto.Models[m.ID] = providerModelDTO{
			ID:           m.ID,
			ProviderID:   pid,
			Name:         m.DisplayName,
			API:          map[string]any{},
			Capabilities: map[string]any{},
			Cost:         map[string]any{},
			Limit:        map[string]any{},
			Options:      map[string]any{},
			Headers:      map[string]any{},
			Variants:     map[string]any{},
		}
		byProvider[pid] = pdto
	}

	ids := make([]string, 0, len(byProvider))
	for pid := range byProvider {
		ids = append(ids, pid)
	}
	slices.Sort(ids)

	providers := make([]providerDTO, 0, len(ids))
	for _, pid := range ids {
		providers = append(providers, byProvider[pid])
	}

	// The default map has one entry per provider that answers the startup spec.
	def := map[string]string{}
	if providerID, modelID, ok := s.sessionModelParts(); ok {
		def[providerID] = modelID
	}
	s.writeJSON(w, http.StatusOK, providersResponseDTO{Providers: providers, Default: def})
}

// GET /provider
func (s *Server) listProviderNames(w http.ResponseWriter, r *http.Request) {
	authed := icodingagent.AuthenticatedProviders(s.services.AgentDir())
	names := make([]string, 0)
	for _, p := range ai.ListProviders() {
		if authed[p] {
			names = append(names, p)
		}
	}
	slices.Sort(names)
	s.writeJSON(w, http.StatusOK, names)
}

// GET /agent
func (s *Server) listAgents(w http.ResponseWriter, r *http.Request) {
	names := []string{"explore", "general", "propose", "build"}
	agents := make([]agentInfoDTO, 0, len(names))
	for _, name := range names {
		agents = append(agents, agentInfoDTO{
			Name:       name,
			Mode:       "primary",
			Permission: []any{},
			Options:    map[string]any{},
		})
	}
	s.writeJSON(w, http.StatusOK, agents)
}

// GET /session/status
func (s *Server) sessionStatus(w http.ResponseWriter, r *http.Request) {
	liveSessions := s.host.Sessions()
	dtos := make([]sessionStatusDTO, 0)
	for _, ls := range liveSessions {
		t := "idle"
		if !ls.Inner().IsIdle() {
			t = "busy"
		}
		dtos = append(dtos, sessionStatusDTO{SessionID: ls.ID(), Type: t})
	}
	s.writeJSON(w, http.StatusOK, dtos)
}

// GET /session/{id}/todo
//
// The newest todowrite result in the transcript, not a list held in this
// process: a session is a file and the server is not, so a client reconnecting
// after a restart must see the todos a conversation visibly has.
func (s *Server) sessionTodo(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	live, _ := s.host.Get(id)
	rec, ok := getSessionRecord(s.sessionDir, id, live)
	if !ok {
		s.writeJSON(w, http.StatusNotFound, errorDTO{Error: "session not found"})
		return
	}
	entries, err := sessionEntries(rec, live, maxLimit)
	if err != nil {
		s.writeJSON(w, http.StatusInternalServerError, errorDTO{Error: err.Error()})
		return
	}
	todos := transcriptTodos(entries)
	if todos == nil {
		todos = []tools.Todo{}
	}
	s.writeJSON(w, http.StatusOK, todos)
}

// transcriptTodos returns the items of the newest todowrite result in entries,
// or nil when the session has none. It walks backwards so the newest match is
// the first one found, and it decodes through JSON because a result read back
// from disk is map[string]any, not the tool's own value.
func transcriptTodos(entries []icodingagent.SessionEntry) []tools.Todo {
	for _, e := range slices.Backward(entries) {
		if e.Base.Type != "message" {
			continue
		}
		var me icodingagent.MessageEntry
		if err := json.Unmarshal(e.Raw(), &me); err != nil {
			continue
		}
		tr := me.Message.ToolResult
		if tr == nil {
			continue
		}
		if todos := todosFromDetails(tr.ToolName, tr.Details); len(todos) > 0 {
			return todos
		}
	}
	return nil
}

// GET /file?path=&directory=
func (s *Server) listFiles(w http.ResponseWriter, r *http.Request) {
	s.serveListFiles(w, r, nil)
}

// serveListFiles is listFiles with a test-only hook. beforeOpen, when non-nil,
// runs in the window between resolving the request path and opening it, so a
// test can swap a component for a symlink deterministically. Production always
// passes nil.
func (s *Server) serveListFiles(w http.ResponseWriter, r *http.Request, beforeOpen func()) {
	if _, errDTO, status := s.directoryParam(r); errDTO != nil {
		s.writeJSON(w, status, errDTO)
		return
	}
	root := s.services.CWD()
	q := r.URL.Query().Get("path")
	if q == "" {
		q = "."
	}

	resolved, err := resolveInRoot(root, q)
	if err != nil {
		s.writeResolveError(w, err)
		return
	}
	// The same root/resolved pair resolveInRoot just validated, so this cannot
	// escape; it reproduces the relative path that check already accepted.
	rel, err := filepath.Rel(root, resolved)
	if err != nil {
		s.writeJSON(w, http.StatusInternalServerError, errorDTO{Error: err.Error()})
		return
	}

	if beforeOpen != nil {
		beforeOpen()
	}

	info, err := os.Stat(resolved)
	if err != nil {
		s.writeJSON(w, http.StatusNotFound, errorDTO{Error: "path not found"})
		return
	}
	if !info.IsDir() {
		s.writeJSON(w, http.StatusBadRequest, errorDTO{Error: "path must be a directory"})
		return
	}

	f, err := os.OpenInRoot(root, rel)
	if err != nil {
		// os.Root exports no escape sentinel, so classify by re-resolving: a
		// fresh refusal is the same refusal (403, or 404 for a path deleted
		// since the stat); a resolve that still succeeds means a genuine open
		// failure, e.g. a mode-0000 directory (500). A racer can only flip
		// 403/404/500, never read outside: the descriptor is confined by
		// os.Root regardless.
		if _, rerr := resolveInRoot(root, q); rerr != nil {
			s.writeResolveError(w, rerr)
		} else {
			s.writeJSON(w, http.StatusInternalServerError, errorDTO{Error: err.Error()})
		}
		return
	}
	defer func() { _ = f.Close() }()

	entries, err := f.ReadDir(-1)
	if err != nil {
		s.writeJSON(w, http.StatusInternalServerError, errorDTO{Error: err.Error()})
		return
	}
	// (*os.File).ReadDir(-1) returns directory order, so sort by name to
	// reproduce the filename order os.ReadDir documented. Which entries
	// survive maxFileEntries depends on it.
	slices.SortFunc(entries, func(a, b os.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })

	dtos := make([]fileNodeDTO, 0)
	for _, entry := range entries {
		if len(dtos) >= maxFileEntries {
			break
		}
		t := "file"
		if entry.IsDir() {
			t = "directory"
		}
		abs := filepath.Join(resolved, entry.Name())
		rel, _ := filepath.Rel(root, abs)
		relSlash := filepath.ToSlash(rel)
		if entry.IsDir() {
			// opencode marks directories with a trailing slash in the
			// project-root-relative path, but not in absolute.
			relSlash += "/"
		}
		dtos = append(dtos, fileNodeDTO{
			Name:     entry.Name(),
			Path:     relSlash,
			Absolute: filepath.ToSlash(abs),
			Type:     t,
		})
	}

	s.writeJSON(w, http.StatusOK, dtos)
}

// writeResolveError answers a resolveInRoot refusal. It is shared with
// /file/content because both handlers read the same three sentinels, and two
// mappings of them is drift nobody would notice.
//
//nolint:gocritic // if-else chain with errors.Is is clearer than switch here
func (s *Server) writeResolveError(w http.ResponseWriter, err error) {
	if errors.Is(err, errMissingPath) {
		s.writeJSON(w, http.StatusNotFound, errorDTO{Error: "path not found"})
	} else if errors.Is(err, errUnreadableLink) || errors.Is(err, errOutsideRoot) {
		s.writeJSON(w, http.StatusForbidden, errorDTO{Error: "path not allowed"})
	} else {
		s.writeJSON(w, http.StatusInternalServerError, errorDTO{Error: err.Error()})
	}
}

// GET /file/content?path=&directory=
//
// 200 with {"content": …} for a text file inside the server root; 404 for a
// path that does not exist; 403 for one that leaves the root, including through
// a symlink; 400 for a directory; 415 for a file that is not text; 413 over
// maxFileContentBytes; 500 for a read that fails. The cap is refused rather than
// applied: a truncated body served as a whole file is a lie the client cannot
// detect, because the cap is the server's and not the file's.
func (s *Server) fileContent(w http.ResponseWriter, r *http.Request) {
	s.serveFileContent(w, r, nil)
}

// serveFileContent is fileContent with a test-only hook. beforeOpen, when
// non-nil, runs in the window between resolving the request path and opening
// it, so a test can swap a component for a symlink deterministically.
// Production always passes nil.
func (s *Server) serveFileContent(w http.ResponseWriter, r *http.Request, beforeOpen func()) {
	if _, errDTO, status := s.directoryParam(r); errDTO != nil {
		s.writeJSON(w, status, errDTO)
		return
	}
	root := s.services.CWD()
	want := r.URL.Query().Get("path")
	resolved, err := resolveInRoot(root, want)
	if err != nil {
		s.writeResolveError(w, err)
		return
	}
	// The same root/resolved pair resolveInRoot just validated, so this cannot
	// escape; it reproduces the relative path that check already accepted.
	rel, err := filepath.Rel(root, resolved)
	if err != nil {
		s.writeJSON(w, http.StatusInternalServerError, errorDTO{Error: err.Error()})
		return
	}

	if beforeOpen != nil {
		beforeOpen()
	}
	// Type and size are checked on the resolved path before the open, so a
	// statically-present FIFO or device is answered 400 without reaching
	// openat. The open below is confined to the root, so a swap that slips
	// past this stat cannot read outside it.
	info, err := os.Stat(resolved)
	if err != nil {
		s.writeJSON(w, http.StatusNotFound, errorDTO{Error: "path not found"})
		return
	}
	if info.IsDir() {
		s.writeJSON(w, http.StatusBadRequest, errorDTO{Error: "path must be a file"})
		return
	}
	if !info.Mode().IsRegular() {
		s.writeJSON(w, http.StatusBadRequest, errorDTO{Error: "path must be a regular file"})
		return
	}
	if info.Size() > maxFileContentBytes {
		s.writeJSON(w, http.StatusRequestEntityTooLarge, errorDTO{Error: tooLargeMessage(info.Size())})
		return
	}

	f, err := os.OpenInRoot(root, rel)
	if err != nil {
		// os.Root exports no escape sentinel, so classify by re-resolving: a
		// fresh refusal is the same refusal (403, or 404 for a path deleted
		// since the stat); a resolve that still succeeds means a genuine open
		// failure, e.g. a mode-0000 file (500). A racer can only flip
		// 403/404/500, never read outside: the descriptor is confined by
		// os.Root regardless.
		if _, rerr := resolveInRoot(root, want); rerr != nil {
			s.writeResolveError(w, rerr)
		} else {
			s.writeJSON(w, http.StatusInternalServerError, errorDTO{Error: err.Error()})
		}
		return
	}
	defer func() { _ = f.Close() }()
	// LimitReader bounds memory: a file that grew between the stat and the read
	// still cannot pull more than the cap plus one byte into the process.
	data, err := io.ReadAll(io.LimitReader(f, maxFileContentBytes+1))
	if err != nil {
		s.writeJSON(w, http.StatusInternalServerError, errorDTO{Error: err.Error()})
		return
	}
	if len(data) > maxFileContentBytes {
		s.writeJSON(w, http.StatusRequestEntityTooLarge, errorDTO{Error: tooLargeMessage(int64(len(data)))})
		return
	}
	if !isTextFile(data) {
		s.writeJSON(w, http.StatusUnsupportedMediaType, errorDTO{Error: "file is not text"})
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]string{"content": string(data)})
}

func tooLargeMessage(size int64) string {
	return fmt.Sprintf("file is %d bytes, over the %d byte limit", size, int64(maxFileContentBytes))
}

// isTextFile reports whether data can be served as a JSON string unchanged. Any
// invalid UTF-8 says no, because json.Encoder substitutes U+FFFD for it and the
// corruption would be silent; a NUL byte in the first 8 KiB says no on its own
// account, because it marks the file as non-text to every other reader.
func isTextFile(data []byte) bool {
	probe := data
	if len(probe) > 8<<10 {
		probe = probe[:8<<10]
	}
	return !bytes.Contains(probe, []byte{0}) && utf8.Valid(data)
}

// GET /find/file?query=&limit=
func (s *Server) findFiles(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("query")
	limit := limitOf(r, defaultLimit, 100)
	root := s.services.CWD()

	matches := make([]fileNodeDTO, 0)
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip errors
		}
		if len(matches) >= maxWalkEntries {
			return io.EOF
		}
		name := d.Name()
		if strings.HasPrefix(name, ".") || name == "node_modules" {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if rel == "." {
			return nil
		}
		matches = append(matches, fileNodeDTO{
			Name:     name,
			Path:     filepath.ToSlash(rel),
			Absolute: filepath.ToSlash(path),
			Type: func() string {
				if d.IsDir() {
					return "directory"
				}
				return "file"
			}(),
		})
		return nil
	})
	if err != nil && !errors.Is(err, io.EOF) {
		s.writeJSON(w, http.StatusInternalServerError, errorDTO{Error: err.Error()})
		return
	}

	dtos := make([]fileNodeDTO, 0)
	if query == "" {
		dtos = matches
	} else {
		// Use tui.FuzzyFilter for fuzzy matching.
		// This is a simplified version - the real FuzzyFilter needs to be called.
		for _, m := range matches {
			if strings.Contains(strings.ToLower(m.Path), strings.ToLower(query)) {
				dtos = append(dtos, m)
			}
		}
	}

	if len(dtos) > limit {
		dtos = dtos[:limit]
	}
	s.writeJSON(w, http.StatusOK, dtos)
}

// GET /file/status
func (s *Server) fileStatus(w http.ResponseWriter, r *http.Request) {
	root, _ := filepath.EvalSymlinks(s.services.CWD())
	paths, err := workingTreePaths(root)
	if err != nil {
		s.writeJSON(w, http.StatusInternalServerError, errorDTO{Error: err.Error()})
		return
	}

	dtos := make([]fileStatusDTO, 0)
	for _, p := range paths {
		if len(dtos) >= maxFileStatusEntries {
			break
		}
		dtos = append(dtos, fileStatusDTO{Path: p})
	}
	s.writeJSON(w, http.StatusOK, dtos)
}

// GET /session/{id}/message?limit=
func (s *Server) sessionMessages(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	limit := limitOf(r, defaultLimit, maxLimit)

	live, _ := s.host.Get(id)
	rec, ok := getSessionRecord(s.sessionDir, id, live)
	if !ok {
		s.writeJSON(w, http.StatusNotFound, errorDTO{Error: "session not found"})
		return
	}

	entries, err := sessionEntries(rec, live, limit)
	if err != nil {
		s.writeJSON(w, http.StatusInternalServerError, errorDTO{Error: err.Error()})
		return
	}

	root, _ := filepath.EvalSymlinks(s.services.CWD())
	cwd := rec.Header.CWD
	if cwd == "" {
		cwd = root
	}
	providerID, modelID, _ := s.sessionModelParts()
	dtos := entriesToMessages(entries, id, cwd, root, providerID, modelID)
	s.writeJSON(w, http.StatusOK, dtos)
}

// sessionEntries is how a handler reads a session's transcript, so /todo and
// /message cannot disagree about one session. The file wins over the live
// session, because a live session with a file holds the same entries in memory
// and the file is the one that survives a restart.
//
// tailEntries may return fewer than limit records if the byte limit
// (maxTailBytes = 16 MiB) is reached before limit complete records are found.
// This happens with very large entries (images, large tool outputs) that exceed
// the per-entry byte budget.
func sessionEntries(rec sessionRecord, live *codinghost.Session, limit int) ([]icodingagent.SessionEntry, error) {
	if rec.HasFile {
		return tailEntries(rec.Path, limit)
	}
	var entries []icodingagent.SessionEntry
	if live != nil {
		entries = live.Inner().Entries()
	}
	if entries == nil {
		entries = make([]icodingagent.SessionEntry, 0)
	}
	if len(entries) > limit {
		entries = entries[len(entries)-limit:]
	}
	return entries, nil
}

// ─── DTO Builders ────────────────────────────────────────────────────────

// sessionModelParts splits the server's startup model spec "provider/model" on
// the first "/". ok is false when modelSpec has no slash (including empty); the
// returned ids are then "", "". It is the single source for the provider/model
// pair used by /config/providers default, the session model, and the message
// model fallback.
func (s *Server) sessionModelParts() (providerID, modelID string, ok bool) {
	providerID, modelID, ok = strings.Cut(s.modelSpec, "/")
	if !ok {
		return "", "", false
	}
	return providerID, modelID, true
}

func (s *Server) buildSessionDTO(rec sessionRecord, live *codinghost.Session) sessionDTO {
	// Compute Updated time: if ModTime is zero (live session with no file yet),
	// fall back to Created. This avoids emitting a negative epoch (year 1).
	updated := rec.ModTime.UnixMilli()
	if rec.ModTime.IsZero() {
		updated = parseRFC3339NanoToMillis(rec.Header.Timestamp)
	}

	root, _ := filepath.EvalSymlinks(s.services.CWD())
	pid := projectID(root)

	dto := sessionDTO{
		ID:        rec.ID,
		Slug:      rec.ID, // slug = id
		ProjectID: pid,
		Directory: rec.Header.CWD,
		Title:     rec.Name,
		Version:   version.Version,
		Summary:   sessionSummary{},
		Time: sessionTime{
			Created:  parseRFC3339NanoToMillis(rec.Header.Timestamp),
			Updated:  updated,
			Archived: rec.Archived,
		},
		Project: projectDTO{ID: pid, Worktree: root},
	}

	// Tokens and cost are always present: a disk-only session emits zeros,
	// which is the honest "wopr does not know". This supersedes the serve-1
	// omission rule for these V1 DTOs (the captured session has both keys).
	if live != nil {
		acc := live.Inner().Inner().Accounting()
		dto.Tokens = sessionTokens{
			Input:  acc.Tokens.Input,
			Output: acc.Tokens.Output,
			Total:  acc.Tokens.Total,
			Cache: tokenCache{
				Read:  acc.Tokens.CacheRead,
				Write: acc.Tokens.CacheWrite,
			},
		}
		dto.Cost = acc.Tokens.Cost
		if m := live.Inner().Model(); m != nil {
			dto.Model = sessionModel{ID: m.ID, ProviderID: codinghost.ModelProviderID(m), Variant: "default"}
		}
	}
	if dto.Model == (sessionModel{}) && rec.ModelID != "" {
		dto.Model = sessionModel{ID: rec.ModelID, ProviderID: rec.ModelProvider, Variant: "default"}
	}
	if dto.Model == (sessionModel{}) {
		providerID, modelID, _ := s.sessionModelParts()
		dto.Model = sessionModel{ID: modelID, ProviderID: providerID, Variant: "default"}
	}

	// Fallback: if directory is empty, use server root.
	if dto.Directory == "" {
		dto.Directory = root
	}

	// Fallback: if updated is zero (live with no file), use created.
	if dto.Time.Updated == 0 {
		dto.Time.Updated = dto.Time.Created
	}

	return dto
}

// messageInfo builds messageInfoDTO for one message. assistant is the
// streamed/persisted assistant message, or nil for a user/toolResult/system
// entry (cost and tokens are then zero, finish is "", and the model falls back
// to fallbackProvider/fallbackModel). created is the message's created time in
// ms. It is called by entriesToMessages and by eventMapper.messageEnd so the
// transcript and the SSE stream cannot drift on shape.
func messageInfo(sessionID, cwd, root, entryID, role, parentID string, created int64,
	assistant *agent.AssistantMessage, fallbackProvider, fallbackModel string) messageInfoDTO {
	info := messageInfoDTO{
		ID:         entryID,
		SessionID:  sessionID,
		Role:       role,
		ParentID:   parentID,
		Path:       messagePath{CWD: cwd, Root: root},
		Time:       messageTime{Created: created},
		ProviderID: fallbackProvider,
		ModelID:    fallbackModel,
	}
	if assistant == nil {
		return info
	}
	if u := assistant.Usage; u != nil {
		info.Cost = u.Cost.Total
		info.Tokens = messageTokens{
			Total:  u.TotalTokens,
			Input:  u.Input,
			Output: u.Output,
			Cache:  tokenCache{Read: u.CacheRead, Write: u.CacheWrite},
		}
		if u.Reasoning != nil {
			info.Tokens.Reasoning = *u.Reasoning
		}
	}
	if assistant.Provider != "" {
		info.ProviderID = assistant.Provider
	}
	if assistant.ModelID != "" {
		info.ModelID = assistant.ModelID
	}
	info.Finish = finishFromStopReason(assistant.StopReason)
	return info
}

// finishFromStopReason maps wopr's stop reason onto the client's finish string.
// toolUse is the one value spelled differently; every other reason passes
// through raw, and an empty reason stays empty.
func finishFromStopReason(reason ai.StopReason) string {
	if reason == ai.StopReasonToolUse {
		return "tool-calls"
	}
	return string(reason)
}

// nearestMessageAncestor returns the id of the nearest ancestor entry whose
// Base.Type is "message", following parentId links through non-message entries.
// Online Context Compact interposes a custom harness-online-context-state-v1
// entry between the user message and the assistant, so the assistant's raw
// parentId names a non-message entry. When wantUser is true only a user-role
// message qualifies; the stream (eventMapper.turnUserID) reports the turn's
// initiating user message for every assistant of a turn, so this is what makes
// an assistant's parentID match the stream, including later assistants of a
// multi-step tool turn whose nearest message ancestor is a toolResult. It
// returns "" when the chain leaves byID or ends without a qualifying ancestor,
// and walks at most len(byID) hops so a cycle cannot spin.
func nearestMessageAncestor(byID map[string]icodingagent.SessionEntry, e icodingagent.SessionEntry, wantUser bool) string {
	for range len(byID) {
		if e.Base.ParentID == nil {
			return ""
		}
		parent, ok := byID[*e.Base.ParentID]
		if !ok {
			return ""
		}
		if parent.Base.Type == "message" {
			if !wantUser {
				return parent.Base.ID
			}
			if me, ok := parent.AsMessage(); ok && me.Message.User != nil {
				return parent.Base.ID
			}
		}
		e = parent
	}
	return ""
}

// entriesToMessages projects transcript entries into messageDTOs. It reads no
// Server state: cwd/root and the model fallback are passed by the caller.
func entriesToMessages(entries []icodingagent.SessionEntry, sessionID, cwd, root,
	fallbackProvider, fallbackModel string) []messageDTO {
	messages := make([]messageDTO, 0)
	var pendingTool map[string]*messageDTO // callID -> messageDTO with pending tool part

	byID := make(map[string]icodingagent.SessionEntry, len(entries))
	for _, e := range entries {
		byID[e.Base.ID] = e
	}

	for _, e := range entries {
		if e.Base.Type != "message" {
			continue
		}
		var me icodingagent.MessageEntry
		if err := json.Unmarshal(e.Raw(), &me); err != nil {
			continue
		}

		// Extract role, content and the assistant message (nil for the others)
		// from the AgentMessage union.
		var (
			role      string
			content   []ai.ContentBlock
			assistant *agent.AssistantMessage
			system    string
		)
		switch {
		case me.Message.User != nil:
			role = me.Message.User.Role
			system = me.Message.User.System
			content = make([]ai.ContentBlock, len(me.Message.User.Content))
			for i, c := range me.Message.User.Content {
				content[i] = c
			}
		case me.Message.Assistant != nil:
			role = me.Message.Assistant.Role
			assistant = me.Message.Assistant
			content = make([]ai.ContentBlock, len(me.Message.Assistant.Content))
			for i, c := range me.Message.Assistant.Content {
				content[i] = c
			}
		case me.Message.ToolResult != nil:
			role = me.Message.ToolResult.Role
			content = make([]ai.ContentBlock, len(me.Message.ToolResult.Content))
			for i, c := range me.Message.ToolResult.Content {
				content[i] = c
			}
		case me.Message.System != nil:
			role = "system"
			content = nil
		default:
			// Custom message (e.g., bashExecution)
			if r, ok := me.Message.Custom["role"].(string); ok {
				role = r
			}
			content = nil
		}

		// Assistant rows resolve to the turn's user message so the transcript
		// matches the stream; toolResult and other rows keep the nearest message.
		// A user row reports no parentID, matching the stream's user
		// message.updated (serve_sse.go messageEnd).
		var parentID string
		if me.Message.User == nil {
			parentID = nearestMessageAncestor(byID, e, assistant != nil)
		}
		created := parseRFC3339NanoToMillis(e.Base.Timestamp)

		msg := messageDTO{
			// A message with no content still marshals parts as [], never null.
			Parts: []partDTO{},
			Info: messageInfo(sessionID, cwd, root, e.Base.ID, role, parentID, created,
				assistant, fallbackProvider, fallbackModel),
		}
		msg.Info.System = system

		for i, block := range content {
			switch b := block.(type) {
			case ai.TextContent:
				msg.Parts = append(msg.Parts, partDTO{
					ID:        partID(e.Base.ID, "text", i, ""),
					MessageID: e.Base.ID,
					SessionID: sessionID,
					Type:      "text",
					Text:      b.Text,
				})
			case ai.ThinkingContent:
				msg.Parts = append(msg.Parts, partDTO{
					ID:        partID(e.Base.ID, "reasoning", i, ""),
					MessageID: e.Base.ID,
					SessionID: sessionID,
					Type:      "reasoning",
					Text:      b.Thinking,
				})
			case ai.ImageContent:
				// The blob resolution with budget is handled in tailEntries.
				// Here we assume the image data is already a data URL or placeholder.
				dataURL := b.Data
				mime := b.MimeType
				if strings.HasPrefix(dataURL, "data:") {
					parts := strings.SplitN(dataURL, ",", 2)
					if len(parts) == 2 {
						mime = strings.TrimPrefix(parts[0], "data:")
						mime = strings.TrimSuffix(mime, ";base64")
					}
				}
				msg.Parts = append(msg.Parts, partDTO{
					ID:        partID(e.Base.ID, "image", i, ""),
					MessageID: e.Base.ID,
					SessionID: sessionID,
					Type:      "file",
					Mime:      mime,
					URL:       dataURL,
				})
			case ai.ToolCall:
				callID := b.ID
				part := partDTO{
					ID:        partID(e.Base.ID, "tool", i, callID),
					MessageID: e.Base.ID,
					SessionID: sessionID,
					Type:      "tool",
					Tool:      b.Name,
					CallID:    callID,
					State:     json.RawMessage(`{"status":"pending"}`),
				}
				msg.Parts = append(msg.Parts, part)
				if pendingTool == nil {
					pendingTool = make(map[string]*messageDTO)
				}
				pendingTool[callID] = &msg
			}
		}

		// Handle toolResult messages: they update a pending tool part or create a new message.
		if role == "toolResult" {
			tr := me.Message.ToolResult
			callID := tr.ToolCallID
			if pendingTool != nil {
				if m, ok := pendingTool[callID]; ok {
					for i := range m.Parts {
						if m.Parts[i].CallID == callID {
							m.Parts[i].State = toolResultState(tr)
							break
						}
					}
					delete(pendingTool, callID)
				} else {
					// Unmatched toolResult: create a new assistant message with one tool part.
					newMsg := messageDTO{
						Info: messageInfo(sessionID, cwd, root, e.Base.ID, "assistant", parentID, created,
							nil, fallbackProvider, fallbackModel),
						Parts: []partDTO{
							{
								ID:        partID(e.Base.ID, "tool", 0, callID),
								MessageID: e.Base.ID,
								SessionID: sessionID,
								Type:      "tool",
								Tool:      tr.ToolName,
								CallID:    callID,
								State:     toolResultState(tr),
							},
						},
					}
					messages = append(messages, newMsg)
				}
			} else {
				// No pending tools at all.
				newMsg := messageDTO{
					Info: messageInfo(sessionID, cwd, root, e.Base.ID, "assistant", parentID, created,
						nil, fallbackProvider, fallbackModel),
					Parts: []partDTO{
						{
							ID:        partID(e.Base.ID, "tool", 0, callID),
							MessageID: e.Base.ID,
							SessionID: sessionID,
							Type:      "tool",
							Tool:      tr.ToolName,
							CallID:    callID,
							State:     toolResultState(tr),
						},
					},
				}
				messages = append(messages, newMsg)
			}
		}

		if role == "bashExecution" {
			msg = messageDTO{
				Info: messageInfo(sessionID, cwd, root, e.Base.ID, "assistant", parentID, created,
					nil, fallbackProvider, fallbackModel),
				Parts: []partDTO{
					{
						// ponytail: the bash execution part keeps its own -bash
						// spelling. It is a custom message the stream never emits,
						// so it has no counterpart to drift from; fold it into the
						// tool form when a stream-side bash part exists.
						ID:        e.Base.ID + "-bash",
						MessageID: e.Base.ID,
						SessionID: sessionID,
						Type:      "tool",
						Tool:      "bash",
						CallID:    e.Base.ID,
						State:     json.RawMessage(`{"status":"completed"}`),
					},
				},
			}
		}

		if len(msg.Parts) > 0 || msg.Info.Role != "" {
			messages = append(messages, msg)
		}
	}

	return messages
}

// toolResultState is a finished tool part's state: its status, its text
// output, and the metadata the tool produced.
//
// todos ride along because this is a recovery surface, not a convenience one:
// the client has no replay and re-fetches history, so a todo panel that only
// filled from the stream would be empty for every client that was not connected
// when the tool ran.
func toolResultState(tr *agent.ToolResultMessage) json.RawMessage {
	var output strings.Builder
	for _, c := range tr.Content {
		if tc, ok := c.(ai.TextContent); ok {
			output.WriteString(tc.Text)
		}
	}
	state := map[string]any{
		"status": "completed",
		"output": output.String(),
	}
	if todos := todosFromDetails(tr.ToolName, tr.Details); len(todos) > 0 {
		state["metadata"] = map[string]any{"todos": todos}
	}
	stateBytes, _ := json.Marshal(state)
	return stateBytes
}

func parseRFC3339NanoToMillis(ts string) int64 {
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return 0
	}
	return t.UnixMilli()
}
