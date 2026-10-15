package serve

import (
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/alexrudloff/wopr/coding/host"
)

type healthResponse struct {
	Healthy bool   `json:"healthy"`
	Version string `json:"version"`
}

// apiHealthResponse is the V2 /api/health body. It has no version field: the
// V1 /global/health keeps version, so the two responses cannot share a type.
type apiHealthResponse struct {
	Healthy bool `json:"healthy"`
}

// Server holds the HTTP server and its dependencies.
type Server struct {
	services   *host.Services
	host       *host.Host
	sessionDir string
	modelSpec  string
	srv        *http.Server
	ln         net.Listener
	// questions holds the ask_user calls waiting for a client to answer them.
	// It is on the Server because GET /question is not session-scoped.
	questions questionStore
	// sseConnections counts the live /global/event streams, which is what the
	// connection cap bounds.
	sseConnections atomic.Int64
}

// New binds addr and returns the server and its listener, so a caller that
// wants the resolved address (an ephemeral test port) can reach it.
func New(addr string, h *host.Host, sessionDir, modelSpec string) (*http.Server, net.Listener, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, nil, err
	}

	// The bus carries Event.Type from the producer to the frame writer as the
	// SSE event name, and part identity is partID's, so the agent→opencode mapper
	// belongs here rather than in coding/host. The mapper is a *Server method, so
	// s must exist before the method value is taken.
	s := &Server{
		services:   h.Services(),
		host:       h,
		sessionDir: sessionDir,
		modelSpec:  modelSpec,
		ln:         ln,
	}
	h.Translate = s.newEventMapper
	s.questions.seed()

	mux := http.NewServeMux()
	// Health
	mux.HandleFunc("GET /global/health", s.health)
	mux.HandleFunc("GET /global/event", s.globalEvent)
	mux.HandleFunc("GET /api/health", s.apiHealth)

	// Sessions
	mux.HandleFunc("GET /session", s.listSessions)
	mux.HandleFunc("GET /experimental/session", s.listExperimentalSessions)
	mux.HandleFunc("POST /session", s.createSession)
	mux.HandleFunc("GET /session/{id}", s.getSession)
	mux.HandleFunc("PATCH /session/{id}", s.patchSession)
	mux.HandleFunc("DELETE /session/{id}", s.deleteSession)
	mux.HandleFunc("POST /session/{id}/fork", s.notImplemented)
	mux.HandleFunc("POST /session/{id}/revert", s.notImplemented)
	mux.HandleFunc("GET /session/{id}/diff", s.notImplemented)
	mux.HandleFunc("GET /session/{id}/todo", s.sessionTodo)
	mux.HandleFunc("GET /session/{id}/message", s.sessionMessages)
	mux.HandleFunc("GET /session/status", s.sessionStatus)
	mux.HandleFunc("POST /session/{id}/prompt_async", s.promptAsync)
	mux.HandleFunc("POST /session/{id}/message", s.sessionMessage)
	mux.HandleFunc("POST /session/{id}/abort", s.sessionAbort)
	// ServeMux rejects a pattern that reuses a wildcard name, and HandleFunc
	// panics on that error, so this route's wildcard is permissionID and not id.
	mux.HandleFunc("POST /session/{id}/permissions/{permissionID}", s.notImplemented)

	// Project
	mux.HandleFunc("GET /project", s.listProjects)
	mux.HandleFunc("GET /project/current", s.getCurrentProject)

	// Config
	mux.HandleFunc("GET /config", s.getConfig)
	mux.HandleFunc("GET /config/providers", s.listProviders)

	// Commands
	mux.HandleFunc("GET /api/command", s.apiCommand)

	// Providers
	mux.HandleFunc("GET /provider", s.listProviderNames)

	// Agents
	mux.HandleFunc("GET /agent", s.listAgents)

	// Permissions
	mux.HandleFunc("GET /permission", s.listPermissions)

	// Questions
	mux.HandleFunc("GET /question", s.listQuestions)
	mux.HandleFunc("POST /question/{id}/reply", s.replyQuestion)
	mux.HandleFunc("POST /question/{id}/reject", s.rejectQuestion)

	// Files
	mux.HandleFunc("GET /file", s.listFiles)
	mux.HandleFunc("GET /file/content", s.fileContent)
	mux.HandleFunc("GET /find/file", s.findFiles)
	mux.HandleFunc("GET /file/status", s.fileStatus)

	s.srv = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	return s.srv, s.ln, nil
}

// Services returns the coding.Services for testing.
func (s *Server) Services() *host.Services {
	return s.services
}
