package serve

import (
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"slices"
	"strings"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
	codinghost "github.com/alexrudloff/wopr/coding/host"
)

// promptBody is the body both prompt endpoints accept. format and agent are
// declared nowhere: json.Decode ignores unknown keys, so a body without them
// accepts both for free.
//
// ponytail: ignored per request, not per session. A JSON-schema-constrained
// retry loop (opencode's StructuredOutputError path) and runtime agent personas
// are two different slices; declaring the fields now would promise behaviour
// nothing implements.
type promptBody struct {
	MessageID string       `json:"messageID,omitempty"`
	Parts     []promptPart `json:"parts"`
	Model     *modelRef    `json:"model,omitempty"`
	System    string       `json:"system,omitempty"`
}

// promptPart is one client part. Text and file are sent; every other type the
// client may send (step markers, reasoning, tool state) carries nothing the
// session can be prompted with and is ignored.
type promptPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
	Mime string `json:"mime,omitempty"`
	URL  string `json:"url,omitempty"`
}

// modelRef is opencode's model reference.
type modelRef struct {
	ProviderID string `json:"providerID"`
	ModelID    string `json:"modelID"`
}

// POST /session/{id}/prompt_async
//
// 204 with no body once the session's single run slot is reserved; a prompt
// while a run is in flight is queued to run next and answered 204; 404 for a
// session this server did not start; 409 only while an ask_user question is
// pending; 400 for a directory that is not the server root, a malformed body, a
// model this server does not offer, or a messageID that already names a
// persisted entry or an in-flight prompt; 413 for a body over maxBodyBytes.
func (s *Server) promptAsync(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.sessionPreconditions(w, r)
	if !ok {
		return
	}
	var body promptBody
	if !s.decodePromptBody(w, r, &body) {
		return
	}
	msgID, ok := s.resolveMessageID(w, sess, body.MessageID)
	if !ok {
		return
	}
	content, errDTO := bodyContent(body.Parts)
	if errDTO != nil {
		s.writeJSON(w, http.StatusBadRequest, errDTO)
		return
	}
	// A pending ask_user holds the run slot while the model waits for the client.
	// Unlike a run in flight, a prompt must not queue behind it: the blocked-input
	// state has no turn to settle, and this 409 is the deliberate invariant
	// docs/plans/2026-10-04-serve-5.md DoD 11 / §Q4 pins.
	if s.questions.hasPending(sess.ID()) {
		s.writePromptError(w, codinghost.ErrSessionBusy) // {"error":"session is busy"}, 409
		return
	}
	// The client names the model per prompt and KKCode sends it here, so it is
	// resolved before the 204 — a request naming a model this server does not
	// offer is refused rather than answered as accepted — and swapped once the
	// run holds the slot, so a prompt queued behind a running turn does not
	// change that turn's model.
	var model *ai.Model
	if body.Model != nil {
		m, errDTO, status := s.resolveModel(sess, *body.Model)
		if errDTO != nil {
			s.writeJSON(w, status, errDTO)
			return
		}
		model = m
	}
	// The per-prompt system text is staged once the slot is held, unconditionally
	// and after a successful model swap, so a model refusal returns before it and
	// a prompt without system clears any pending value.
	if err := sess.PromptContent(r.Context(), content, msgID, func() error {
		if model != nil {
			if err := sess.Inner().SetModel(model); err != nil {
				return err
			}
		}
		sess.Inner().Agent().SetNextUserSystem(body.System)
		return nil
	}); err != nil {
		s.writePromptError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// POST /session/{id}/message
//
// 200 with the finished assistant message as {info, parts}. 404, 400 and 413 as
// for prompt_async, plus 400 for a model this server does not offer. Unlike
// prompt_async it blocks for the run slot rather than queueing, so a busy
// session is 409.
func (s *Server) sessionMessage(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.sessionPreconditions(w, r)
	if !ok {
		return
	}
	var body promptBody
	if !s.decodePromptBody(w, r, &body) {
		return
	}
	msgID, ok := s.resolveMessageID(w, sess, body.MessageID)
	if !ok {
		return
	}
	content, errDTO := bodyContent(body.Parts)
	if errDTO != nil {
		s.writeJSON(w, http.StatusBadRequest, errDTO)
		return
	}

	type outcome struct {
		entryID  string
		runError error
		// errDTO and status carry a refusal from before the turn started, which
		// is a client's own bad model rather than a failure of the run.
		errDTO *errorDTO
		status int
	}
	settled := make(chan outcome, 1)
	go func() {
		// The model is a session-wide mutation, so it is applied once the run
		// slot is held and before the turn starts: a busy session is answered
		// 409 with its model untouched, and a turn never runs on the model the
		// previous request chose.
		var errDTO *errorDTO
		var status int
		msgs, runError := sess.PromptContentWait(r.Context(), content, msgID, func() error {
			if body.Model != nil {
				errDTO, status = s.applyModel(sess, *body.Model)
				if errDTO != nil {
					return errRefused // refusal returns before the set: no pending value
				}
			}
			sess.Inner().Agent().SetNextUserSystem(body.System)
			return nil
		})
		settled <- outcome{
			entryID:  lastPersistedAssistantID(sess, msgs),
			runError: runError,
			errDTO:   errDTO,
			status:   status,
		}
	}()

	select {
	case <-r.Context().Done():
		// The client left. The run continues: the context it was given is
		// detached from this request, and the reservation is released when the
		// run settles.
		return
	case got := <-settled:
		if got.errDTO != nil {
			s.writeJSON(w, got.status, got.errDTO)
			return
		}
		if got.runError != nil {
			// MessageWithParts has no error field and a failed run's assistant
			// message does not say why it failed, so the reason reaches the
			// client as a status instead of a 200 that hides it. A refused
			// reservation is 409 and comes out of here too: it produced no
			// assistant message, and reading the entry first would answer 500
			// "run produced no persisted assistant message" for a request that
			// was never accepted.
			s.writePromptError(w, got.runError)
			return
		}
		if got.entryID == "" {
			s.writeJSON(w, http.StatusInternalServerError, errorDTO{Error: "run produced no persisted assistant message"})
			return
		}
		dto, found := s.assistantMessageDTO(sess, got.entryID)
		if !found {
			s.writeJSON(w, http.StatusInternalServerError, errorDTO{Error: "assistant message is not readable from the transcript"})
			return
		}
		s.writeJSON(w, http.StatusOK, dto)
	}
}

// errRefused ends a PromptContentWait whose before hook declined the request.
// It never reaches a client: the hook records the status to answer instead, and
// the error only tells the handler not to report a run failure.
var errRefused = errors.New("serve: the request was refused before the turn started")

// POST /session/{id}/abort
//
// 200 with a JSON boolean, whether the session was running or idle, matching
// opencode (the client decodes the body and rejects an empty non-JSON reply).
// The cancel is delivered synchronously; the drain is reported by the next
// session.status.
func (s *Server) sessionAbort(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.sessionPreconditions(w, r)
	if !ok {
		return
	}
	sess.Abort()
	s.writeJSON(w, http.StatusOK, true)
}

// sessionPreconditions applies the checks every session-scoped write shares: the
// directory must be the server root, and the session must be one this server
// started. A disk-only session is 404 here because Host.Start cannot adopt an
// existing file, so a prompt to one could never run.
func (s *Server) sessionPreconditions(w http.ResponseWriter, r *http.Request) (*codinghost.Session, bool) {
	if _, errDTO, status := s.directoryParam(r); errDTO != nil {
		s.writeJSON(w, status, errDTO)
		return nil, false
	}
	sess, ok := s.host.Get(r.PathValue("id"))
	if !ok {
		s.writeJSON(w, http.StatusNotFound, errorDTO{Error: "session not found"})
		return nil, false
	}
	return sess, true
}

// decodePromptBody enforces maxBodyBytes and decodes the prompt body. It
// answers the request itself on failure: a truncated body and a malformed one
// are different client bugs, and MaxBytesReader is the only thing in the tree
// that can tell them apart.
func (s *Server) decodePromptBody(w http.ResponseWriter, r *http.Request, body *promptBody) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(body); err != nil {
		if _, tooLarge := errors.AsType[*http.MaxBytesError](err); tooLarge {
			s.writeJSON(w, http.StatusRequestEntityTooLarge, errorDTO{Error: "request body too large"})
			return false
		}
		s.writeJSON(w, http.StatusBadRequest, errorDTO{Error: "invalid body"})
		return false
	}
	return true
}

// resolveMessageID validates the optional client-minted user message id. A
// value not beginning with "msg" is 400, matching opencode's schema. A value
// already persisted in this session is 400 here: a fast pre-check. Host.Session
// is authoritative for the in-flight window (a reserved-but-unpersisted id),
// which this cannot see. ok=false means the response was already written.
func (s *Server) resolveMessageID(w http.ResponseWriter, sess *codinghost.Session, id string) (string, bool) {
	if id == "" {
		return "", true
	}
	if !strings.HasPrefix(id, "msg") {
		s.writeJSON(w, http.StatusBadRequest, errorDTO{Error: "invalid messageID"})
		return "", false
	}
	if _, exists := sess.Inner().EntryByID(id); exists {
		s.writeJSON(w, http.StatusBadRequest, errorDTO{Error: "messageID already exists"})
		return "", false
	}
	return id, true
}

// bodyContent converts client parts to user content blocks. A file part's data
// URL is split into the mime type and the base64 payload ai.ImageContent
// carries.
func bodyContent(parts []promptPart) ([]ai.UserContentBlock, *errorDTO) {
	content := make([]ai.UserContentBlock, 0, len(parts))
	for _, p := range parts {
		switch p.Type {
		case "text":
			content = append(content, ai.TextContent{Text: p.Text})
		case "file":
			content = append(content, ai.ImageContent{MimeType: dataURLMime(p.URL, p.Mime), Data: dataURLPayload(p.URL)})
		}
	}
	if len(content) == 0 {
		return nil, &errorDTO{Error: "message has no text or file part"}
	}
	return content, nil
}

func dataURLMime(url, fallback string) string {
	header, _, ok := strings.Cut(url, ",")
	if !ok || !strings.HasPrefix(header, "data:") {
		return fallback
	}
	return strings.TrimSuffix(strings.TrimPrefix(header, "data:"), ";base64")
}

func dataURLPayload(url string) string {
	if _, payload, ok := strings.Cut(url, ","); ok && strings.HasPrefix(url, "data:") {
		return payload
	}
	return url
}

// resolveModel turns the client's model reference into the model this server
// would run. It mutates nothing, so a caller can refuse a request that names a
// model this server does not offer before accepting it.
func (s *Server) resolveModel(sess *codinghost.Session, ref modelRef) (*ai.Model, *errorDTO, int) {
	models, err := codinghost.AvailableModels(s.services, sess.Inner().Model())
	if err != nil {
		return nil, &errorDTO{Error: err.Error()}, http.StatusInternalServerError
	}
	i := slices.IndexFunc(models, func(candidate *ai.Model) bool {
		return codinghost.ModelProviderID(candidate) == ref.ProviderID && candidate.ID == ref.ModelID
	})
	if i < 0 {
		return nil, &errorDTO{Error: "model not found: " + ref.ProviderID + "/" + ref.ModelID}, http.StatusBadRequest
	}
	return models[i], nil, 0
}

// applyModel is resolveModel plus the swap, for the routes that can only
// resolve once the run slot is held. SetModel has already stored the new model
// when its audit append fails, so that case answers 500 and is recorded rather
// than hidden.
func (s *Server) applyModel(sess *codinghost.Session, ref modelRef) (*errorDTO, int) {
	model, errDTO, status := s.resolveModel(sess, ref)
	if errDTO != nil {
		return errDTO, status
	}
	if err := sess.Inner().SetModel(model); err != nil {
		return &errorDTO{Error: err.Error()}, http.StatusInternalServerError
	}
	return nil, 0
}

// writePromptError maps a reservation failure to its status.
func (s *Server) writePromptError(w http.ResponseWriter, err error) {
	if errors.Is(err, codinghost.ErrSessionBusy) {
		s.writeJSON(w, http.StatusConflict, errorDTO{Error: "session is busy"})
		return
	}
	if errors.Is(err, codinghost.ErrDuplicateMessageID) {
		s.writeJSON(w, http.StatusBadRequest, errorDTO{Error: "messageID already exists"})
		return
	}
	s.writeJSON(w, http.StatusInternalServerError, errorDTO{Error: err.Error()})
}

// lastPersistedAssistantID resolves the transcript entry id of the newest
// assistant message the run produced. TurnEndEvent.MessageEntryID never
// reaches Session.Events(), so this is the reconciliation source: the message
// is persisted before its message_end reaches any listener.
func lastPersistedAssistantID(sess *codinghost.Session, msgs []agent.AgentMessage) string {
	entryID := ""
	for _, m := range msgs {
		if m.Assistant == nil {
			continue
		}
		if id, ok := sess.Inner().MessageEntryID(m); ok {
			entryID = id
		}
	}
	return entryID
}

// assistantMessageDTO renders one transcript entry through entriesToMessages,
// so the /message response and GET /session/{id}/message cannot disagree on
// part IDs, the tool-part state upgrade, or the info shape. It passes the full
// transcript so the projection can resolve an assistant entry's parentID past
// interposed non-message entries (e.g. Online Context Compact metadata); the
// caller selects its entry by id from the result.
func (s *Server) assistantMessageDTO(sess *codinghost.Session, entryID string) (messageDTO, bool) {
	entries := sess.Inner().Entries()
	root, _ := filepath.EvalSymlinks(s.services.CWD())
	cwd := sess.Inner().CWD()
	if cwd == "" {
		cwd = root
	}
	providerID, modelID, _ := s.sessionModelParts()
	for _, dto := range entriesToMessages(entries, sess.ID(), cwd, root, providerID, modelID) {
		if dto.Info.ID == entryID {
			return dto, true
		}
	}
	return messageDTO{}, false
}
