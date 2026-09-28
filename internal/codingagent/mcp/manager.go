package mcp

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Server states reported by Status.
const (
	StateIdle     = "idle"
	StateReady    = "ready"
	StateFailed   = "failed"
	StateExited   = "exited"
	StateDisabled = "disabled"
)

// Manager owns the configured servers' connections. Servers connect on first
// use; a server that exits or fails to start is retried on the next use.
type Manager struct {
	cfg     Config
	cwd     string
	version string
	// Environ and Lookup default to the process environment.
	Environ func() []string
	Lookup  func(string) (string, bool)
	// Transport, when set, replaces transport construction (tests).
	Transport func(name string, cfg ServerConfig) (sdk.Transport, error)
	// OnTools is called with a server's tool list after each fetch.
	OnTools func(server string, cfg ServerConfig, tools []*sdk.Tool)

	client  *sdk.Client
	mu      sync.Mutex
	servers map[string]*server
}

type server struct {
	name string
	cfg  ServerConfig
	// mu serializes connect and state changes. conn, done and stale are
	// also written under Manager.mu, which markStale reads them under.
	mu     sync.Mutex
	conn   *sdk.ClientSession
	done   chan struct{}
	tools  []*sdk.Tool
	stale  bool
	state  string
	err    error
	stderr *tailBuffer
	starts int
}

// NewManager creates a manager for cfg. cwd is the stdio servers' default
// working directory and version is reported to servers.
func NewManager(cfg Config, cwd, version string) *Manager {
	m := &Manager{cfg: cfg, cwd: cwd, version: version, Environ: os.Environ, Lookup: os.LookupEnv, servers: map[string]*server{}}
	for name, sc := range cfg.Servers {
		state := StateIdle
		if sc.Disabled {
			state = StateDisabled
		}
		m.servers[name] = &server{name: name, cfg: sc, state: state, stderr: &tailBuffer{max: 4096}}
	}
	version = cmp.Or(version, "dev")
	m.client = sdk.NewClient(&sdk.Implementation{Name: "wopr", Version: version}, &sdk.ClientOptions{
		ToolListChangedHandler: func(_ context.Context, req *sdk.ToolListChangedRequest) { m.markStale(req.Session) },
	})
	return m
}

// Config returns the configuration the manager was built from.
func (m *Manager) Config() Config { return m.cfg }

// Names returns the enabled server names, sorted.
func (m *Manager) Names() []string { return m.cfg.Names() }

func (m *Manager) markStale(cs *sdk.ClientSession) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.servers {
		if s.conn == cs {
			s.stale = true
		}
	}
}

func (m *Manager) server(name string) (*server, error) {
	m.mu.Lock()
	s := m.servers[name]
	m.mu.Unlock()
	if s == nil {
		return nil, fmt.Errorf("unknown MCP server %q; configured: %s", name, orNone(m.Names()))
	}
	if s.cfg.Disabled {
		return nil, fmt.Errorf("MCP server %q is disabled", name)
	}
	return s, nil
}

func orNone(names []string) string {
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}

// Tools returns a server's tools, connecting it first when needed.
func (m *Manager) Tools(ctx context.Context, name string) ([]*sdk.Tool, error) {
	_, tools, err := m.ensure(ctx, name)
	return tools, err
}

// ensure returns a live session and its tools, starting the server when it is
// idle, failed or exited, and refreshing a stale tool list.
func (m *Manager) ensure(ctx context.Context, name string) (*sdk.ClientSession, []*sdk.Tool, error) {
	s, err := m.server(name)
	if err != nil {
		return nil, nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn != nil && !closed(s.done) {
		m.mu.Lock()
		stale := s.stale
		s.stale = false
		m.mu.Unlock()
		if stale {
			if err := m.fetchTools(ctx, s); err != nil {
				return nil, nil, err
			}
		}
		return s.conn, s.tools, nil
	}
	if err := m.connect(ctx, s); err != nil {
		return nil, nil, err
	}
	return s.conn, s.tools, nil
}

func closed(done chan struct{}) bool {
	if done == nil {
		return true
	}
	select {
	case <-done:
		return true
	default:
		return false
	}
}

// connect starts s and lists its tools. The caller holds s.mu.
func (m *Manager) connect(ctx context.Context, s *server) error {
	s.starts++
	s.stderr.Reset()
	startCtx, cancel := context.WithTimeout(ctx, s.cfg.startTimeout())
	defer cancel()
	conn, err := m.dial(startCtx, s)
	if err != nil {
		s.state, s.err = StateFailed, m.startError(s, startCtx, err)
		return s.err
	}
	done := make(chan struct{})
	m.mu.Lock()
	s.conn, s.done, s.stale = conn, done, false
	m.mu.Unlock()
	go func() {
		waitErr := conn.Wait()
		close(done)
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.conn == conn && s.state == StateReady {
			s.state = StateExited
			s.err = exitError(waitErr, s.stderr.String())
		}
	}()
	if err := m.fetchTools(startCtx, s); err != nil {
		_ = conn.Close()
		s.state, s.err = StateFailed, m.startError(s, startCtx, err)
		return s.err
	}
	s.state, s.err = StateReady, nil
	return nil
}

func (m *Manager) startError(s *server, ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		err = fmt.Errorf("did not start within %s", s.cfg.startTimeout())
	}
	msg := fmt.Sprintf("MCP server %q failed to start: %v", s.name, err)
	if tail := strings.TrimSpace(s.stderr.String()); tail != "" {
		msg += "\nstderr: " + tail
	}
	return errors.New(msg)
}

func exitError(err error, stderr string) error {
	msg := "connection closed"
	if err != nil {
		msg = err.Error()
	}
	if tail := strings.TrimSpace(stderr); tail != "" {
		msg += "; stderr: " + tail
	}
	return errors.New(msg)
}

func (m *Manager) dial(ctx context.Context, s *server) (*sdk.ClientSession, error) {
	cfg := s.cfg.expanded(m.Lookup)
	if m.Transport != nil {
		t, err := m.Transport(s.name, cfg)
		if err != nil {
			return nil, err
		}
		return m.client.Connect(ctx, t, nil)
	}
	switch cfg.Transport() {
	case TransportStdio:
		cmd := exec.Command(cfg.Command, cfg.Args...)
		cmd.Env = serverEnv(m.Environ(), cfg.Env)
		cmd.Dir = cmp.Or(cfg.Cwd, m.cwd)
		cmd.Stderr = s.stderr
		return m.client.Connect(ctx, &sdk.CommandTransport{Command: cmd, TerminateDuration: 2 * time.Second}, nil)
	case TransportSSE:
		return m.client.Connect(ctx, &sdk.SSEClientTransport{Endpoint: cfg.URL, HTTPClient: headerClient(cfg.Headers)}, nil)
	}
	conn, err := m.client.Connect(ctx, &sdk.StreamableClientTransport{Endpoint: cfg.URL, HTTPClient: headerClient(cfg.Headers), DisableStandaloneSSE: true, MaxRetries: -1}, nil)
	if err != nil && s.cfg.Type == "" && ctx.Err() == nil {
		// An untyped url may be a legacy SSE server.
		if sse, sseErr := m.client.Connect(ctx, &sdk.SSEClientTransport{Endpoint: cfg.URL, HTTPClient: headerClient(cfg.Headers)}, nil); sseErr == nil {
			return sse, nil
		}
	}
	return conn, err
}

// fetchTools lists s's tools. The caller holds s.mu.
func (m *Manager) fetchTools(ctx context.Context, s *server) error {
	var tools []*sdk.Tool
	for tool, err := range s.conn.Tools(ctx, nil) {
		if err != nil {
			return fmt.Errorf("list tools: %w", err)
		}
		tools = append(tools, tool)
	}
	slices.SortFunc(tools, func(a, b *sdk.Tool) int { return strings.Compare(a.Name, b.Name) })
	s.tools = tools
	if m.OnTools != nil {
		m.OnTools(s.name, s.cfg, tools)
	}
	return nil
}

// Call runs one tool. An unknown tool name errors with the server's tool
// names; a call on a server that exited restarts it once.
func (m *Manager) Call(ctx context.Context, name, tool string, args map[string]any) (*sdk.CallToolResult, error) {
	conn, tools, err := m.ensure(ctx, name)
	if err != nil {
		return nil, err
	}
	if _, err := findTool(tools, name, tool); err != nil {
		return nil, err
	}
	s, _ := m.server(name)
	callCtx, cancel := context.WithTimeout(ctx, s.cfg.callTimeout())
	defer cancel()
	result, err := conn.CallTool(callCtx, &sdk.CallToolParams{Name: tool, Arguments: args})
	if err == nil {
		return result, nil
	}
	switch {
	case ctx.Err() != nil:
		return nil, ctx.Err()
	case errors.Is(callCtx.Err(), context.DeadlineExceeded):
		return nil, fmt.Errorf("MCP tool %s/%s timed out after %s", name, tool, s.cfg.callTimeout())
	}
	var rpcErr *jsonrpc.Error
	transportErr := !errors.As(err, &rpcErr)
	s.mu.Lock()
	done := s.done
	s.mu.Unlock()
	if transportErr && done != nil {
		// A dying server's read error can arrive before its session ends.
		select {
		case <-done:
		case <-time.After(500 * time.Millisecond):
		}
	}
	s.mu.Lock()
	dead := s.conn == conn && (closed(s.done) || errors.Is(err, sdk.ErrConnectionClosed) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF))
	if dead {
		_ = conn.Close()
		s.state = StateExited
		s.err = exitError(err, s.stderr.String())
		err = s.err
	}
	s.mu.Unlock()
	if dead {
		return nil, fmt.Errorf("MCP server %q exited (%w); it restarts on the next call", name, err)
	}
	return nil, err
}

// findTool returns server's tool named name, or an error listing the tools.
func findTool(tools []*sdk.Tool, server, name string) (*sdk.Tool, error) {
	i := slices.IndexFunc(tools, func(t *sdk.Tool) bool { return t.Name == name })
	if i < 0 {
		return nil, fmt.Errorf("MCP server %q has no tool %q; tools: %s", server, name, orNone(toolNames(tools)))
	}
	return tools[i], nil
}

func toolNames(tools []*sdk.Tool) []string {
	names := make([]string, len(tools))
	for i, t := range tools {
		names[i] = t.Name
	}
	return names
}

// Restart closes a server (all servers when name is empty) so the next use
// starts it again.
func (m *Manager) Restart(name string) error {
	names := []string{name}
	if name == "" {
		names = m.Names()
	}
	for _, n := range names {
		s, err := m.server(n)
		if err != nil {
			return err
		}
		s.mu.Lock()
		if s.conn != nil {
			_ = s.conn.Close()
		}
		m.mu.Lock()
		s.conn, s.done = nil, nil
		m.mu.Unlock()
		s.tools, s.state, s.err = nil, StateIdle, nil
		s.mu.Unlock()
	}
	return nil
}

// Close stops every server.
func (m *Manager) Close() {
	m.mu.Lock()
	servers := slices.Collect(maps.Values(m.servers))
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, s := range servers {
		wg.Go(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			if s.conn != nil {
				_ = s.conn.Close()
				m.mu.Lock()
				s.conn = nil
				m.mu.Unlock()
			}
		})
	}
	wg.Wait()
}

// ServerStatus is one server's state for /mcp.
type ServerStatus struct {
	Name      string
	Transport string
	Source    string
	State     string
	Err       string
	Tools     int
	Starts    int
}

// Status reports every configured server, sorted by name. It never starts a
// server.
func (m *Manager) Status() []ServerStatus {
	m.mu.Lock()
	servers := slices.Collect(maps.Values(m.servers))
	m.mu.Unlock()
	slices.SortFunc(servers, func(a, b *server) int { return strings.Compare(a.name, b.name) })
	out := make([]ServerStatus, 0, len(servers))
	for _, s := range servers {
		if !s.mu.TryLock() {
			out = append(out, ServerStatus{Name: s.name, Transport: s.cfg.Transport(), Source: s.cfg.Source, State: "starting", Starts: s.starts})
			continue
		}
		st := ServerStatus{Name: s.name, Transport: s.cfg.Transport(), Source: s.cfg.Source, State: s.state, Tools: len(s.tools), Starts: s.starts}
		if s.err != nil {
			st.Err = s.err.Error()
		}
		s.mu.Unlock()
		out = append(out, st)
	}
	return out
}

// headerClient returns an HTTP client that adds headers to every request.
func headerClient(headers map[string]string) *http.Client {
	if len(headers) == 0 {
		return &http.Client{}
	}
	return &http.Client{Transport: headerTransport{base: http.DefaultTransport, headers: headers}}
}

type headerTransport struct {
	base    http.RoundTripper
	headers map[string]string
}

func (t headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	for k, v := range t.headers {
		req.Header.Set(k, v)
	}
	return t.base.RoundTrip(req)
}

// tailBuffer keeps the last max bytes written, for crash messages.
type tailBuffer struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	if over := len(b.buf) - b.max; over > 0 {
		b.buf = append(b.buf[:0], b.buf[over:]...)
	}
	return len(p), nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}

func (b *tailBuffer) Reset() {
	b.mu.Lock()
	b.buf = b.buf[:0]
	b.mu.Unlock()
}
