package serve

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	codinghost "github.com/alexrudloff/wopr/coding/host"
	"github.com/alexrudloff/wopr/internal/codingagent/askuser"
)

// questionDTO is one entry of GET /question: the id the client echoes back to
// reply or reject, plus the two fields its card renders.
type questionDTO struct {
	ID        string           `json:"id"`
	SessionID string           `json:"sessionID"`
	Question  string           `json:"question"`
	Options   []askuser.Option `json:"options"`
}

// pendingQuestion is one blocked ask_user call: the session it belongs to, the
// question the model asked, and the single slot its answer arrives in.
type pendingQuestion struct {
	id        string
	sessionID string
	question  askuser.Question
	answers   chan askuser.Answer
}

// questionStore is the pending questions, one per blocked ask_user call, and
// the counter that names them.
//
// It needs no cap: a run holds its session's single run slot for its whole
// duration and asks at most one question at a time, so the entries are bounded
// by the sessions that are mid-run rather than by conversation length.
type questionStore struct {
	mu sync.Mutex
	m  map[string]*pendingQuestion
	// next mints ids. It is clock-seeded rather than zero-based so a client
	// still holding an id from before a restart cannot answer an unrelated new
	// question with it.
	next atomic.Uint64
}

// seed prepares an empty store whose ids start at the current millisecond.
func (q *questionStore) seed() {
	q.m = make(map[string]*pendingQuestion)
	q.next.Store(uint64(time.Now().UnixMilli()))
}

// add registers a question and returns its id with the entry.
func (q *questionStore) add(sessionID string, question askuser.Question) (string, *pendingQuestion) {
	p := &pendingQuestion{
		sessionID: sessionID,
		question:  question,
		answers:   make(chan askuser.Answer, 1),
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	p.id = "q" + strconv.FormatUint(q.next.Add(1), 10)
	q.m[p.id] = p
	return p.id, p
}

// take removes the entry for id and hands it to the caller.
//
// The lookup and the delete are one critical section, and that is the whole
// point: two concurrent replies to one id must produce exactly one winner and
// one 404. If the delete happened under a second lock acquisition, the loser's
// send would block forever, because the single slot is already taken by the
// winner and nothing reads it again.
func (q *questionStore) take(id string) (*pendingQuestion, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	p, ok := q.m[id]
	if !ok {
		return nil, false
	}
	delete(q.m, id)
	return p, true
}

// list returns every pending question, oldest first.
func (q *questionStore) list() []questionDTO {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]questionDTO, 0, len(q.m))
	for _, p := range q.m {
		out = append(out, questionDTO{
			ID:        p.id,
			SessionID: p.sessionID,
			Question:  p.question.Question,
			Options:   p.question.Options,
		})
	}
	// Ids come from one counter, so sorting by id gives a fixed order and a poll
	// never reshuffles the client's list. It is the order they were asked in
	// only while every id has the same number of digits.
	slices.SortFunc(out, func(a, b questionDTO) int { return strings.Compare(a.ID, b.ID) })
	return out
}

// hasPending reports whether sessionID has a question awaiting an answer.
func (q *questionStore) hasPending(sessionID string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, p := range q.m {
		if p.sessionID == sessionID {
			return true
		}
	}
	return false
}

// ask is the asker every served session installs: it registers the question,
// publishes it, and blocks until a reply, a reject, or the run's cancellation.
// Whichever arrives first wins.
func (s *Server) ask(ctx context.Context, sessionID string, q askuser.Question) (askuser.Answer, error) {
	id, pending := s.questions.add(sessionID, q)
	s.publishQuestion(eventQuestionAsked, pending, nil)

	select {
	case a := <-pending.answers:
		return a, nil
	case <-ctx.Done():
		// The run was aborted. Leaving the entry behind would render a card the
		// client can never answer and never see go away, because it has no
		// replay and no expiry.
		if _, still := s.questions.take(id); still {
			s.publishQuestion(eventQuestionRejected, pending, &askuser.Answer{Declined: true})
			return askuser.Answer{}, ctx.Err()
		}
		// Another caller claimed the entry and will send its answer; honour it,
		// because that caller already returned 200 and published the outcome.
		return <-pending.answers, nil
	}
}

// publishQuestion puts one question event on the bus. The payload is the same
// entry GET /question serves, so a card built from the stream and one built from
// a poll cannot disagree.
func (s *Server) publishQuestion(name string, p *pendingQuestion, answer *askuser.Answer) {
	_ = s.host.Publish(codinghost.Event{Type: name, Session: p.sessionID, Data: sseQuestion{
		questionDTO: questionDTO{
			ID:        p.id,
			SessionID: p.sessionID,
			Question:  p.question.Question,
			Options:   p.question.Options,
		},
		Answer: answer,
	}})
}

// GET /question
//
// 200 with every pending question, and an empty array when there is none. The
// client polls this to fill a list, and 404 is how it learns the host is gone,
// so "nothing to answer" must not spend that status.
func (s *Server) listQuestions(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, s.questions.list())
}

// POST /question/{id}/reply
//
// 200 with no body once the answer is on its way to the blocked ask_user; 404
// for an id that is gone — answered, rejected, or aborted with its run; 400 for
// a body that names neither a choice nor other text, or both; 413 over
// maxBodyBytes.
func (s *Server) replyQuestion(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Choice string `json:"choice"`
		Other  string `json:"other"`
	}
	if !s.decodeQuestionBody(w, r, &body) {
		return
	}
	answer := askuser.Answer{
		Choice: strings.TrimSpace(body.Choice),
		Other:  strings.TrimSpace(body.Other),
	}
	if (answer.Choice == "") == (answer.Other == "") {
		s.writeJSON(w, http.StatusBadRequest, errorDTO{Error: "exactly one of choice or other must be set"})
		return
	}

	pending, ok := s.questions.take(r.PathValue("id"))
	if !ok {
		s.writeJSON(w, http.StatusNotFound, errorDTO{Error: "question not found"})
		return
	}
	// The channel is buffered for one and this is the only sender: take removed
	// every other claimant, so this cannot block.
	pending.answers <- answer
	s.publishQuestion(eventQuestionReplied, pending, &answer)
	w.WriteHeader(http.StatusOK)
}

// POST /question/{id}/reject
//
// 200 with no body; 404 for an id that is gone. The answer is fixed, so the
// body carries nothing this handler reads: {"declined":true} and an empty body
// mean the same thing.
func (s *Server) rejectQuestion(w http.ResponseWriter, r *http.Request) {
	pending, ok := s.questions.take(r.PathValue("id"))
	if !ok {
		s.writeJSON(w, http.StatusNotFound, errorDTO{Error: "question not found"})
		return
	}
	answer := askuser.Answer{Declined: true}
	pending.answers <- answer
	s.publishQuestion(eventQuestionRejected, pending, &answer)
	w.WriteHeader(http.StatusOK)
}

// decodeQuestionBody enforces maxBodyBytes and decodes a question answer. It
// answers the request itself on failure: a truncated body and a malformed one
// are different client bugs, and MaxBytesReader is the only thing in the tree
// that tells them apart.
func (s *Server) decodeQuestionBody(w http.ResponseWriter, r *http.Request, body any) bool {
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
