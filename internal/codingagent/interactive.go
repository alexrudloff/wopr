package codingagent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	wopr "github.com/alexrudloff/wopr"
	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/internal/codingagent/llama"
	"github.com/alexrudloff/wopr/internal/codingagent/tools"
	"github.com/alexrudloff/wopr/tui"
)

type expandableCustomMessageComponent interface {
	tui.Component
	SetExpanded(bool)
}

// pendingToolArg accumulates streaming tool call fragments for one tool call index.
type pendingToolArg struct {
	id   string
	name string
	args strings.Builder
}

type compactionQueueMode string

const (
	compactionQueueSteer    compactionQueueMode = "steer"
	compactionQueueFollowUp compactionQueueMode = "followUp"
)

type compactionQueuedMessage struct {
	text   string
	images []ai.ImageContent
	mode   compactionQueueMode
}

// InteractiveMode runs the full interactive TUI session.
type InteractiveMode struct {
	opts InteractiveOptions
	// setup is the setup screen while /setup runs; it replaces the home
	// screen.
	setup   *setupScreen
	tuiInst *tui.TUI
	agent   *agent.Agent
	// shareCommand runs gh for /share; nil means runShareCommand.
	shareCommand shareCommandRunner
	// The live session is owned by the SessionHandle (coding.Session.inner) and
	// is the single source of truth. Read it via m.currentSession() so display
	// (/session, /tree, /compact) and persistence (the agent's OnMessagePersist
	// hook, which also reads coding.Session.inner) can never diverge across a
	// /new, /clone, or /resume swap: a swap goes through SessionHandle.ReplaceInner
	// and is visible everywhere at once.
	// resourceSourceInfo annotates loaded prompts/skills/themes
	// with their package/top-level origin. Populated via the callback in
	// InteractiveOptions and refreshed on /reload.
	resourceSourceInfo map[string]ResourceSourceInfo

	// UI components
	chatContainer *tui.Container
	editor        *tui.Editor
	statusLine    *StatusLine

	// State
	isIdle bool
	// turnActive is true from the synchronous commit of a turn in runPromptTurn
	// until that turn's goroutine returns. Unlike isIdle (reset via a queued
	// runOnMain, so it can read stale-false right after a turn ends) and unlike
	// Agent.IsStreaming() (false during the pre-stream window: goroutine
	// dispatch, before_agent_start hook, pre-prompt auto-compaction), it marks
	// exactly the interval in which a run goroutine exists to drain the steering
	// queue. hasActiveAgentTurn uses it so a submit in the pre-stream window
	// steers instead of starting a second concurrent turn. Set on the main
	// loop, cleared on the turn goroutine; read from both, hence atomic.
	turnActive atomic.Bool
	// queueMu serializes the decision to queue input into the active run
	// (turnActive set) against settleTurn clearing turnActive, so input is
	// never queued into a run that has already made its last queue check.
	queueMu sync.Mutex
	// runGen counts runs started on the owner loop; a run's queued UI
	// cleanup applies only while it is still the latest run. Owner loop only.
	runGen uint64
	// turnSettled is closed when the active run settles; waitForIdle waits
	// on it. Guarded by queueMu; nil while no run is active.
	turnSettled chan struct{}

	requestExit atomic.Bool // /quit / /exit sets this; input loop notices and returns. Atomic: may be set off the owner loop.
	// restartRequested asks the caller to re-exec the upgraded binary on
	// restartSession after Run returns.
	restartRequested bool
	restartSession   string
	// availableUpdate is a newer release the startup check found.
	availableUpdate string
	fatalRuntime    atomic.Bool // fatal session replacement errors exit 1 after the input loop restores the terminal.

	// suspended is true while the session is parked by SIGTSTP. SIGINT is
	// ignored then.
	suspended atomic.Bool

	// reloadIssues collects tool reload failures from the most recent /reload
	// so ReloadDiagnostics can surface them in-session instead of dropping them
	// to a TUI-clobbered stderr. Reset at the
	// start of each Reload. Written and read on the main loop.
	reloadIssues []string
	abortCtx     context.Context
	abortFn      context.CancelFunc
	// runCtx is the root context passed to Run. Stored so goroutines that must
	// start a turn outside the input loop (e.g. flushing the compaction queue
	// from the agent-event handler) have a live parent context.
	runCtx           context.Context
	backgroundCtx    context.Context
	backgroundCancel context.CancelFunc
	backgroundTasks  sync.WaitGroup
	clipboardCtx     context.Context
	clipboardReads   *sync.WaitGroup
	// openURL opens a URL in the default browser for OSC 8 hyperlink activation.
	// Injected so a test can supply a capturing opener; nil falls back to
	// openBrowser.
	openURL func(url string) error
	// confirm asks a yes/no question; nil uses a dialog. Tests inject it.
	confirm func(prompt string) bool
	// copyClipboard writes user-requested text to the host clipboard.
	// Injected for deterministic tests; nil uses the platform helper.
	copyClipboard func(text string) error
	// rendererOut, when non-nil, is the fixed-size output the renderer writes
	// to instead of the process terminal, so a test can drive the real
	// render/input path hermetically; production leaves it nil.
	rendererOut                    io.Writer
	statusContainer                *tui.Container // standalone fallback when the editor does not embed status
	pendingMessagesContainer       *tui.Container // queued steering/follow-up display (between chat and status)
	activeStatusIndicator          *tui.StatusIndicator
	activeWorkingIndicatorEmbedded bool
	statusLastFrame                time.Time
	spinnerIntervalCh              chan time.Duration
	// spinnerTickQueued is set while a spinner tick waits in uiTaskCh.
	spinnerTickQueued atomic.Bool

	branchSummaryCancel context.CancelFunc

	// Bash-prefix execution state.
	// bashCancel cancels the currently-running `!cmd` (Esc routes
	// here when isBashRunning). pendingBashBlocks queue components
	// dispatched while the agent is mid-turn; they promote to chat
	// after the bash goroutine finishes.
	bashCancel          context.CancelFunc
	pendingBashBlocks   []*tui.BashExecutionBlock
	pendingBashBlocksMu sync.Mutex

	// Double-Esc opens /tree. Tracks the wall-clock time of the last idle
	// Esc so a second within 500ms fires the action.
	lastEscapeTime time.Time

	// Tracks the wall-clock time of the last Ctrl+C so a second within
	// 500ms exits: the first Ctrl+C clears the editor, a second within 500ms
	// shuts down.
	lastSigintTime time.Time

	// activeLoginCancel cancels an in-progress background OAuth login
	// (github-copilot, anthropic device/PKCE flows) so Esc or Ctrl+C aborts
	// the polling window, matching the "Ctrl+C to cancel" hint. The codex
	// flow cancels through its modal dialog instead. Guarded by loginMu
	// because a login can be started from the main loop (/login) or a turn
	// goroutine (401 re-auth), while the key handler that cancels it runs on
	// the main loop. activeLoginGen lets a completing login clear only its
	// own cancel, never a newer one's.
	loginMu           sync.Mutex
	activeLoginCancel context.CancelFunc
	activeLoginGen    int

	// Last assistant message text (for /copy).
	lastAssistantText string
	lastStatusSpacer  *tui.Spacer
	lastStatusText    *tui.Text

	// skipNextUserMessageText suppresses the MessageStartEvent for the prompt
	// already rendered synchronously by handleSubmit. Steering/follow-up user
	// messages are only surfaced by agent events, so they must not be skipped.
	skipNextUserMessageText string

	// Countdown goroutine cancel function for auto-retry.
	// Called on AutoRetryEndEvent or when a new retry starts.
	retryCountdownStop func()

	// Slash-command registry. Owns the builtins. Constructed in Run().
	slashRegistry *SlashRegistry

	// App-level keybinding registry loaded from <agentDir>/keybindings.json.
	keybindings *KeybindingsManager

	// Fullscreen presentation state: the latest routing decision, the spinner
	// clock, the pending leader key and interrupt, and the home placeholder.
	route             routeInfo
	spinnerEpoch      time.Time
	leaderDeadline    time.Time
	interruptDeadline time.Time
	// firstPrompt titles an unnamed session; modifiedFiles lists the files
	// edited or written this session for the sidebar.
	firstPrompt   string
	modifiedFiles []fileChange
	// side is the sidebar's data beyond the status line snapshot.
	side sidebarState
	// measure measures models in the background (see setup_measure.go).
	measure     *modelMeasurer
	measureOnce sync.Once
	// lastSidebarSnapshot is the sidebar snapshot last saved to the session.
	lastSidebarSnapshot []byte
	// homeEpoch is when the home screen first drew; its animations run
	// from it.
	homeEpoch time.Time
	// cursorEpoch restarts the prompt cursor's blink; cursorWasLit is the
	// blink state last drawn.
	cursorEpoch  time.Time
	cursorWasLit bool
	// Home screen data: the random seed of the machine's LED rhythms, the
	// example in the placeholder, and the providers with credentials. homeShown tracks when the idle home screen appears so the
	// data refreshes each time.
	homeSeed        uint64
	promptExample   int
	authedProviders map[string]bool
	homeShown       bool
	// toastHandle is the toast on screen, if any; dialogDepth counts open
	// dialogs, which dim the screen; sidebarMode overrides the sidebar's
	// width-based visibility.
	toastHandle *tui.OverlayHandle
	// toastUntil is when the showing toast closes.
	toastUntil  time.Time
	dialogDepth int
	// pendingQuestion is an ask_user question waiting for the editor to
	// empty (see ask_dialog.go).
	pendingQuestion *pendingQuestion
	// goalChecking is set while GoalNext runs off the owner loop (see
	// goal_mode.go).
	goalChecking bool
	sidebarMode  string

	// toolByID holds only pending calls so completion updates the streaming card and releases its ID for later calls. toolOrder retains transcript order for Ctrl+O; toolsExpanded is the global expansion choice.
	toolMu        sync.Mutex
	toolByID      map[string]*tui.ToolExecutionComponent
	toolStarts    map[string]time.Time
	toolOrder     []*tui.ToolExecutionComponent
	bashOrder     []*tui.BashExecutionBlock // row 2.8a: Ctrl+O drives bash blocks too
	toolsExpanded bool

	// pendingArgs accumulates ToolCallDelta fragments keyed by index.
	// Used to build progressive args preview during streaming.
	pendingArgs map[int]*pendingToolArg

	// Compaction state.
	// isCompacting gates message queueing during an in-flight compact.
	// compactionQueue holds messages typed while compaction runs; drained
	// by flushCompactionQueue on CompactionEndEvent.
	// compactionOrder tracks rendered CompactionSummaryComponents so
	// Ctrl+O expand/collapse applies to them alongside tool components.
	isCompacting    bool
	compactionQueue []compactionQueuedMessage
	compactionOrder []*tui.CompactionSummaryComponent

	// uiTaskCh carries closures posted by background workers (e.g. the async
	// autocomplete fd search) to run on the main input loop. Editor and
	// component state is mutated only on that goroutine, single-threaded
	// with keystroke handling, so workers never race the lock-free TUI
	// component tree.
	uiTaskCh chan func()

	// eventCh is the agent's live event stream (m.opts.SessionHandle.Events()).
	// The inputLoop select drains it and calls handleAgentEvent on the main
	// goroutine. evCurrentBlock is that handler's
	// cross-event state (one assistant block per message), only ever touched
	// on the main goroutine.
	eventCh        <-chan agent.AgentEvent
	evCurrentBlock *tui.AssistantMessageBlock

	// branchSummaryOrder tracks rendered BranchSummaryComponents so
	// Ctrl+O expand/collapse applies to them alongside tool components.
	branchSummaryOrder []*tui.BranchSummaryComponent
	customMessageOrder []expandableCustomMessageComponent

	// terminalInputListeners: raw terminal input handlers (debug hotkey, games).
	// Each handler receives raw keystrokes and may consume them.
	terminalInputMu         sync.Mutex
	terminalInputListenerID uint64
	terminalInputListeners  []terminalInputListener
	modalInputMu            sync.RWMutex
	modalInputCh            chan []byte
	modalInputDepth         int
	// modalDoneCh is closed by setModalInputChannel(nil) when a modal tears
	// down. The stdin reader selects on it so a send to the outgoing modal
	// channel can never block forever when the modal stops draining mid-spam.
	modalDoneCh chan struct{}
	// modalChangedCh is closed (and replaced) on every setModalInputChannel
	// call. The input pump selects on it while holding a chunk for the main
	// loop so an arming modal can reclaim that chunk instead of the pump
	// deadlocking on the unbuffered readCh that a busy main loop cannot drain.
	modalChangedCh chan struct{}

	// Inference timing for the spinner. workStart is the wall-clock
	// time the current LLM call began; zero when idle.
	workStart time.Time

	// rawRestore is the cleanup closure returned by tui.EnterRawMode.
	// The external-editor flow uses it to temporarily restore
	// cooked mode, hand the terminal to $EDITOR, then re-enter raw
	// mode for wopr.
	rawRestore func()

	// File-based prompt templates loaded from
	// <agentDir>/prompts/ (user) and <cwd>/.wopr/prompts/ (project).
	// Populated in Run() before the input loop starts. Slash dispatch
	// falls through to template expansion when no builtin matches.
	promptTemplates   []PromptTemplate
	promptDiagnostics []ResourceDiagnostic

	// Thinking level and visibility state.
	// thinkingLevel is the user-facing cycle string ("off"/"low"/"medium"/"high").
	// hideThinking hides thinking blocks.
	// assistantBlocks tracks every AssistantMessageBlock added during this session
	// so Ctrl+T can call SetHiddenThinking on all of them: the single coupling
	// point replacing the old thinkingOrder[] slice.
	thinkingLevel string
	hideThinking  bool
	// tabbing is set while Tab moves between modes and models; the stop it
	// lands on is saved when the next prompt is sent (routingSavePending),
	// not every stop it passes.
	tabbing, routingSavePending bool
	assistantBlocks             []*tui.AssistantMessageBlock
	userBlocks                  []*tui.UserMessageBlock
	outputPad                   int

	// editorContainer wraps the editor so a selector/input/overlay can take the
	// editor's slot via SetChildren.
	editorContainer *tui.Container
	// transcriptScrollView is the scrollable conversation view.
	transcriptScrollView *tui.ScrollView
	// tuiTornDown guards stopInteractiveTui so the renderer is stopped and the
	// transcript view disposed exactly once across the explicit-quit, ctx.Done,
	// and input-error exit paths.
	tuiTornDown bool

	// scheduledRender holds the latest throttled render the renderer handed to
	// dispatchScheduledRender; renderWakeCh (capacity 1) wakes the main loop to
	// run it. A pending render coalesces with later ones instead of competing
	// for uiTaskCh space, so it is never dropped.
	// managedToolStatusStarted records that a managed-tool status report was
	// shown: the first one is preceded by a spacer.
	managedToolStatusStarted bool

	scheduledRenderMu sync.Mutex
	scheduledRender   func()
	renderWakeCh      chan struct{}
}

// postUITask hands a closure to the main input loop to run
// single-threaded with keystroke handling. Used by background workers
// (async autocomplete) so they never mutate the lock-free TUI component
// tree from their own goroutine. The send is non-blocking: if the queue
// is saturated or the loop has exited, the (cosmetic) task is dropped
// and the next keystroke refreshes, avoiding worker-goroutine leaks on
// shutdown. It reports whether fn was queued.
func (m *InteractiveMode) postUITask(fn func()) bool {
	if fn == nil {
		return false
	}
	select {
	case m.uiTaskCh <- fn:
		return true
	default:
		return false
	}
}

// dispatchScheduledRender is the renderer's dispatch hook for throttled
// renders. It never blocks the timer goroutine and never drops the render:
// the newest render replaces a pending one, and the main loop runs it on its
// next wake.
func (m *InteractiveMode) dispatchScheduledRender(render func()) {
	m.scheduledRenderMu.Lock()
	m.scheduledRender = render
	m.scheduledRenderMu.Unlock()
	select {
	case m.renderWakeCh <- struct{}{}:
	default:
	}
}

// installRenderDispatcher runs throttled scheduled renders on the main input
// loop instead of the throttle-timer goroutine, so doRender never reads the
// lock-free component tree concurrently with the main loop's mutations (e.g.
// the working-spinner frame advanced by tickSpinner).
func (m *InteractiveMode) installRenderDispatcher() {
	m.tuiInst.SetRenderDispatcher(m.dispatchScheduledRender)
}

// runScheduledRender runs the pending throttled render, if any, on the main
// loop.
func (m *InteractiveMode) runScheduledRender() {
	m.scheduledRenderMu.Lock()
	render := m.scheduledRender
	m.scheduledRender = nil
	m.scheduledRenderMu.Unlock()
	if render != nil {
		render()
	}
}

// runOnMain hands fn to the main input loop and blocks until the loop accepts
// it (backpressure) or ctx is cancelled (shutdown/abort). Unlike postUITask it
// never drops fn, so it is the tool for state the loop must not miss: streamed
// bash output, login results, turn-end state. It does not wait for fn to run;
// the loop executes queued tasks FIFO, so a caller that posts in order sees its
// mutations applied in order. Callers must not hold a lock the loop also takes.
// A worker's result is delivered to the loop rather than mutating UI state
// from the worker (Bubble Tea's p.Send has the same blocking-with-cancel
// semantics).
func (m *InteractiveMode) runOnMain(ctx context.Context, fn func()) {
	_ = m.postToMain(ctx, fn)
}

// errOwnerLoopUnavailable reports a task the owner loop could not accept.
var errOwnerLoopUnavailable = errors.New("interactive mode is not accepting work")

// postToMain is runOnMain for callers that must report a task the loop never
// accepted, such as a background prompt: it never drops fn, and
// returns an error only when ctx ends first.
func (m *InteractiveMode) postToMain(ctx context.Context, fn func()) error {
	if fn == nil {
		return nil
	}
	if ctx == nil {
		// No cancellation source (only happens in tests with no running loop).
		// Degrade to the non-blocking post rather than risk a permanent block.
		if !m.postUITask(fn) {
			return errOwnerLoopUnavailable
		}
		return nil
	}
	select {
	case m.uiTaskCh <- fn:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("%w: %w", errOwnerLoopUnavailable, context.Cause(ctx))
	}
}

func (m *InteractiveMode) showCacheMissNotices() bool {
	return m.settings().GetShowCacheMissNotices()
}

// settings returns the merged settings, or the defaults without a settings
// manager.
func (m *InteractiveMode) settings() Settings { return m.opts.SettingsManager.Get() }

func (m *InteractiveMode) setModalInputChannelLocked(ch chan []byte) {
	// Every route change wakes the input pump if it is parked holding a chunk
	// for a busy main loop (see routeInputChunk): an arming modal must be able
	// to reclaim that chunk rather than let it deadlock on readCh.
	if m.modalChangedCh != nil {
		close(m.modalChangedCh)
	}
	m.modalChangedCh = make(chan struct{})
	if ch == nil {
		// Tear-down: wake any reader blocked sending to the outgoing modal
		// channel so it re-routes the parsed sequence to the next focus target.
		if m.modalDoneCh != nil {
			close(m.modalDoneCh)
			m.modalDoneCh = nil
		}
		m.modalInputCh = nil
		return
	}
	if m.modalDoneCh != nil {
		close(m.modalDoneCh)
	}
	m.modalInputCh = ch
	m.modalDoneCh = make(chan struct{})
}

// acquireModalInputChannel returns the shared parsed-input route for a modal
// scope. Nested modal flows reuse it, so focus can move from a parent selector
// to a child selector without racing a second stdin reader or dropping type-ahead.
func (m *InteractiveMode) acquireModalInputChannel() (chan []byte, func()) {
	m.modalInputMu.Lock()
	if m.modalInputCh == nil {
		m.setModalInputChannelLocked(make(chan []byte))
	}
	m.modalInputDepth++
	ch := m.modalInputCh
	m.modalInputMu.Unlock()

	var once sync.Once
	return ch, func() {
		once.Do(func() {
			m.modalInputMu.Lock()
			defer m.modalInputMu.Unlock()
			m.modalInputDepth--
			if m.modalInputDepth == 0 {
				m.setModalInputChannelLocked(nil)
			}
		})
	}
}

// modalRouteWatch is modalRoute plus the change-signal channel, captured in
// one locked snapshot so the input pump never misses a route change that
// races its select.
func (m *InteractiveMode) modalRouteWatch() (chan []byte, chan struct{}, <-chan struct{}) {
	m.modalInputMu.Lock()
	defer m.modalInputMu.Unlock()
	// The change signal must exist before the first modal ever opens: a
	// nil channel never fires, so a chunk read before then (a terminal's
	// startup reply) would wait on the main loop forever while the main
	// loop waits in that first modal for input.
	if m.modalChangedCh == nil {
		m.modalChangedCh = make(chan struct{})
	}
	return m.modalInputCh, m.modalDoneCh, m.modalChangedCh
}

// routeInputChunk delivers one stdin chunk to the active modal selector, or
// to the main loop via readCh when no modal is active. If a modal arms while
// the chunk is waiting for the main loop to drain readCh, the chunk is
// re-routed to that modal. It returns the ticket of a chunk the main loop
// took, which settles once the chunk's terminal-input listeners are done.
//
// This closes the /tree → "Summarize branch?" freeze: between two chained
// modal selectors the route is briefly nil, and the whole chain runs
// synchronously on the main loop, so the main loop is not draining readCh. A
// keystroke read in that window must not park the pump forever on the
// unbuffered readCh: when the next selector arms, modalChangedCh fires and
// the chunk routes to it.
func (m *InteractiveMode) routeInputChunk(ctx context.Context, chunk []byte, readCh chan<- inputChunk) *inputTicket {
	ticket := newInputTicket()
	for {
		modalCh, modalDone, changed := m.modalRouteWatch()
		if modalCh != nil {
			select {
			case modalCh <- chunk:
				return nil
			case <-modalDone:
				// Focus changed while this parsed sequence was waiting. Re-read
				// the route and deliver it to the newly focused component.
				continue
			case <-ctx.Done():
				return nil
			}
		}
		select {
		case readCh <- inputChunk{data: chunk, ticket: ticket}:
			return ticket
		case <-changed:
			// A modal armed (or the route otherwise changed) while the busy
			// main loop could not receive on readCh. Re-evaluate and deliver
			// to the modal instead of deadlocking.
			continue
		case <-ctx.Done():
			return nil
		}
	}
}

// InteractiveOptions configures the interactive mode.
type InteractiveOptions struct {
	CWD      string
	AgentDir string
	Model    *ai.Model
	// SettingsManager holds the settings, including this run's flag
	// overrides. Wired by main.go via coding.Services.
	SettingsManager *SettingsManager
	// SystemPrompt overrides the assembled system prompt. If empty,
	// callers should build one via prompts.BuildDefaultPrompt and pass
	// it in.
	SystemPrompt string
	// AllowedTools restricts which tool names may execute. nil means no
	// restriction; a non-nil empty map blocks every tool. Set from the
	// `tools:` allowlist on agent .md frontmatter.
	AllowedTools map[string]struct{}
	// ActiveBuiltinTools, when non-nil, restricts which built-in coding
	// tools are active (caller tools unaffected). nil = all
	// built-in tools. CLI default [read, bash, edit, write]; grep/find/ls
	// inactive unless requested via --tools.
	ActiveBuiltinTools map[string]struct{}
	// ExcludedTools is a denylist of tool names made non-callable, gating
	// built-in and caller tools alike.
	ExcludedTools map[string]struct{}
	// NoBuiltinTools hides built-in tools while leaving custom tools.
	NoBuiltinTools bool
	// PromptPaths lists additional prompt-template directories.
	// Directories are searched after the default <agentDir>/prompts/ and
	// <cwd>/.wopr/prompts/ (last-wins by name, same as project scope).
	PromptPaths []string
	// SessionDir overrides the on-disk session directory used by /new,
	// /resume, and other interactive session-management flows.
	SessionDir string
	// SessionID specifies an exact session ID for new sessions.
	// Set by --session-id. When non-empty and no
	// existing session matches, Create uses this ID instead of
	// generating a random one.
	SessionID string
	// ThemePaths lists additional theme files/directories to load.
	ThemePaths []string
	// NoPromptTemplates disables prompt template discovery.
	NoPromptTemplates bool
	// NoThemes disables custom theme discovery from disk.
	NoThemes bool

	// SessionHandle, when non-nil, is the pre-constructed agent +
	// on-disk session that InteractiveMode wraps for the TUI.
	// Constructed by the caller via coding.NewSession from the public
	// SDK package; main.go does the bridging between coding.* and
	// internal/codingagent.*. When nil, Run() returns an error: the
	// SDK is the only supported construction path post-Chunk F.
	//
	// Mutually exclusive with ResumePath: if SessionHandle is set,
	// the caller is responsible for having loaded the session via
	// coding.NewSession's ResumePath field.
	SessionHandle InteractiveSessionHandle

	// ContextUsage returns the live Session projection estimate for footer updates.
	ContextUsage func() (tokens *int, contextWindow int)

	// ModelBuilder constructs an *ai.Model from a "<provider>/<id>"
	// spec. Wired by main.go to coding.BuildModel: the public SDK
	// can't be imported from internal/codingagent (cycle), so the
	// caller injects the constructor. nil disables /model switching
	// (handler falls back to print-only).
	ModelBuilder func(spec string) (*ai.Model, error)

	// RequestAuthRuntime is the composed checkAuth/getAuth surface used by
	// warning-only auth checks. Wired by main.go from the same credential and
	// provider configuration used for model requests.
	RequestAuthRuntime *RequestAuthRuntime

	// ModelRegistry is the model registry for auth-aware operations
	// (Refresh, GetAvailable, HasConfiguredAuth). Wired by main.go.
	// nil is safe; post-login refresh is skipped.
	ModelRegistry *ModelRegistry

	// ResumePath, when non-empty, is loaded as the active session at
	// startup. The agent's message history is rebuilt from
	// session.BuildContext() and the transcript is replayed inline so
	// the user sees prior conversation.
	ResumePath string
	// InitialMessage, when non-empty, is auto-submitted to the agent
	// immediately after the TUI initialises. Sourced by main.go from
	// positional args + piped stdin so users can `wopr "hello"`,
	// `wopr < file.txt`, or pipe + run.
	InitialMessage string
	InitialImages  []ai.ImageContent
	// InitialMessages are the positional messages after the first. Each is
	// sent as its own prompt once the previous one settles.
	InitialMessages []string

	// AppVersion is wopr's release version. It records the last changelog
	// version seen and gates the startup "what's new" notification.
	AppVersion string

	// BinaryUpdateChecker, if set, is invoked asynchronously at startup and
	// returns a newer-release notice for the wopr binary itself, or nil. The
	// notice names the command that applies it (/upgrade).
	BinaryUpdateChecker func() *BinaryUpdate

	// ReleaseSource is where /upgrade reads releases; nil disables /upgrade.
	ReleaseSource func() ReleaseSource

	// Llama is the built-in llama.cpp provider: /llama manages its router
	// models, /login configures it, and startup refreshes its catalog.
	Llama *llama.Host
	// OfflineMode skips startup network catalog refreshes, like WOPR_OFFLINE.
	OfflineMode bool

	// ResourceSourceInfoProvider returns source metadata for currently known
	// prompts/skills/themes, keyed by absolute path. Used to enrich
	// verbose startup output and /reload diagnostics with package origin.
	ResourceSourceInfoProvider func() map[string]ResourceSourceInfo
	// ReloadResourceProvider recomputes settings-backed prompt/theme/skill/
	// context-file inputs on /reload instead of reusing startup snapshots.
	ReloadResourceProvider func() ReloadResourceSnapshot

	// Verbose forces verbose startup output (overrides quietStartup).
	Verbose bool

	// Skills is the loaded set of skill definitions, used to expand
	// /skill:name commands in user prompts.
	Skills []*SkillDef
	// RebuildSystemPrompt reconstructs the base system prompt + structured
	// prompt options after /reload from the current skills/context files.
	RebuildSystemPrompt func(skills []*SkillDef, contextFiles []ContextFile) string
	// SkillPaths are the source paths from which Skills were loaded.
	// Used by /reload to re-discover skills from disk. If empty, skills
	// are not reloaded (only the initial set from startup is used).
	SkillPaths []string
	// NoSkills disables skill loading and reload.
	NoSkills bool

	// ContextFiles lists the loaded AGENTS.md/CLAUDE.md project context files.
	// Shown in the startup banner when verbose/non-quiet.
	ContextFiles []ContextFile

	// ThinkingLevel is the initial thinking level override from --thinking.
	// Empty means use default (settings or "medium").
	ThinkingLevel string

	// NoModelWarning, when non-empty, is rendered as a yellow warning
	// line immediately before the welcome banner, e.g. "Warning: No models
	// available. ..." when the user starts without `--model` and has no
	// detectable credentials.
	NoModelWarning string
	// RunSetup opens setup at launch (wopr setup). Setup also opens on its
	// own on a first run.
	RunSetup bool
	// ModelFromFlag reports a model named on the command line: an explicit
	// choice, so a first run doesn't open setup.
	ModelFromFlag bool
	// StartupDiagnostics are shown in the chat after the welcome banner.
	StartupDiagnostics []AgentSessionRuntimeDiagnostic

	// StartupMark, when non-nil, is called at key phases during Run()
	// for startup timing. See cmd/wopr/startup_trace.go.
	StartupMark func(label string)
}

func (m *InteractiveMode) currentSystemPrompt() string {
	return m.opts.SystemPrompt
}

// NewInteractiveMode creates an interactive session.
func NewInteractiveMode(opts InteractiveOptions) *InteractiveMode {
	if opts.CWD == "" {
		opts.CWD, _ = os.Getwd()
	}
	if opts.AgentDir == "" {
		opts.AgentDir = DefaultAgentDir()
	}
	tui.SetCapabilityOverrides(opts.SettingsManager.Get().GetTerminalCapabilityOverrides())

	m := &InteractiveMode{
		opts:               opts,
		isIdle:             true,
		resourceSourceInfo: map[string]ResourceSourceInfo{},
		toolByID:           make(map[string]*tui.ToolExecutionComponent),
		toolStarts:         make(map[string]time.Time),
		uiTaskCh:           make(chan func(), 64),
		renderWakeCh:       make(chan struct{}, 1),
		pendingArgs:        make(map[int]*pendingToolArg),
		spinnerIntervalCh:  make(chan time.Duration, 1),
		outputPad:          opts.SettingsManager.Get().GetOutputPad(),
	}
	return m
}

func (m *InteractiveMode) newSessionManager() *SessionManager {
	var sm *SessionManager
	if m.opts.SessionDir != "" {
		sm = NewSessionManagerWithDir(m.opts.CWD, m.opts.SessionDir)
	} else {
		sm = NewSessionManager(m.opts.CWD)
	}
	return sm
}

// currentSession returns the live on-disk session, owned by the SessionHandle.
// It is the single source of truth: a /new, /clone, or /resume swap calls
// SessionHandle.ReplaceInner, and both this accessor and the agent's
// persistence hook read the same underlying coding.Session.inner, so the
// displayed session and the persistence target are always identical.
func (m *InteractiveMode) currentSession() *Session {
	if m.opts.SessionHandle == nil {
		return nil
	}
	return m.opts.SessionHandle.Inner()
}

// quoteIfNeeded shell-quotes a value only when it contains characters
// outside the safe set.
func quoteIfNeeded(value string) string {
	if value != "" && !quoteUnsafeRE.MatchString(value) {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

var quoteUnsafeRE = regexp.MustCompile(`[^a-zA-Z0-9_\-./~:@]`)

// resumeCommand builds the "wopr [--session-dir <dir>] --session <id>" string
// for a persisted session, or "" when the session has no on-disk file.
// Pure so it can be unit-tested without a live session or terminal.
func resumeCommand(sessionPath, sessionID, sessionDir string) string {
	if sessionPath == "" {
		return "" // not persisted (--no-session)
	}
	if _, err := os.Stat(sessionPath); err != nil {
		return ""
	}
	parts := []string{"wopr"}
	if sessionDir != "" {
		parts = append(parts, "--session-dir", quoteIfNeeded(sessionDir))
	}
	parts = append(parts, "--session", sessionID)
	return strings.Join(parts, " ")
}

// printResumeHint writes "To resume this session: wopr --session <id>" after the
// terminal is restored on an interactive quit (not on signal shutdown). The
// TTY check is implicit: interactive mode only runs on a TTY (raw mode is
// required).
func (m *InteractiveMode) printResumeHint() {
	if m.currentSession() == nil {
		return
	}
	cmd := resumeCommand(m.currentSession().Path(), m.currentSession().ID(), m.opts.SessionDir)
	if cmd == "" {
		return
	}
	_, _ = fmt.Printf("\x1b[2mTo resume this session:\x1b[22m %s\n", cmd)
}

// Run renders the initial editor and footer even with quiet startup, then blocks in the interactive loop until the user exits.
func (m *InteractiveMode) Run(ctx context.Context) (err error) {
	// Registered first so it runs last, after the deferred TUI teardown has
	// restored the terminal.
	defer func() {
		if value := recover(); value != nil {
			m.uncaughtCrash(value, debug.Stack(), os.Stderr)
			err = ErrInteractiveCrashed
		}
	}()
	m.runCtx = ctx
	m.backgroundCtx, m.backgroundCancel = context.WithCancel(ctx)
	defer func() {
		m.backgroundCancel()
		m.backgroundTasks.Wait()
		m.backgroundCtx = nil
	}()
	mark := m.opts.StartupMark
	if mark == nil {
		mark = func(string) {} // no-op
	}
	// Detect dark/light terminal and set theme colors.
	// If Settings.Theme is explicitly set, resolve it; otherwise auto-detect
	// from COLORFGBG.
	tui.SetThemeSetting(m.settings().Theme)
	if !m.opts.NoThemes {
		registry := tui.ActiveThemeRegistry()
		themesDir := filepath.Join(m.opts.AgentDir, "themes")
		if _, err := os.Stat(themesDir); err == nil {
			_ = registry.LoadDir(themesDir) // best-effort; don't block startup
		}
		for _, themePath := range m.opts.ThemePaths {
			if err := loadThemePath(registry, themePath); err != nil {
				fmt.Fprintf(os.Stderr, "theme load: %v\n", err)
			}
		}
	}
	// Re-apply theme in case it was a custom theme name.
	if m.settings().Theme != "" {
		tui.SetThemeSetting(m.settings().Theme)
	}
	mark("theme-detected")

	// createInteractiveTui returns the renderer's owner-scoped cleanup, which
	// runs on every Run return.
	defer m.createInteractiveTui(ctx)()
	m.tuiInst.SetShowHardwareCursor(m.settings().GetShowHardwareCursor())
	m.installRenderDispatcher()
	mark("pre-raw-mode")
	restore, err := tui.EnterRawMode()
	if err != nil {
		return fmt.Errorf("interactive: raw mode: %w", err)
	}
	defer func() {
		if m.rawRestore != nil {
			m.rawRestore()
		}
	}()
	mark("raw-mode-entered")
	m.rawRestore = restore
	m.tuiInst.HideCursor()
	defer m.tuiInst.ShowCursor()
	// Centralized, idempotent renderer teardown. Registered after ShowCursor so
	// LIFO runs it FIRST on any non-SIGHUP return (ctx.Done, input error,
	// explicit quit): the renderer leaves the alternate screen and restores
	// mouse/autowrap before the cursor and cooked mode are restored. The
	// SIGHUP path exits via os.Exit(129), which skips defers and correctly
	// avoids writing to a dead terminal.
	defer m.stopInteractiveTui()

	m.keybindings = NewKeybindingsManager(m.opts.AgentDir)
	m.chatContainer = tui.NewContainer()
	mark("tui-layout-built")
	m.editor = tui.NewEditor()
	m.editor.OnChange = m.onEditorChange
	m.editor.EmbedWorkingStatus = true
	defer m.clearStatusIndicator("")
	m.editorContainer = tui.NewContainer()
	m.editorContainer.Add(m.editor)
	m.editor.Focused = true
	m.tuiInst.SetFocus(m.editor)
	m.editor.SetPaddingX(m.settings().GetEditorPaddingX())
	m.editor.SetAutocompleteMaxVisible(m.settings().GetAutocompleteMaxVisible())
	// Run the @-file fd search off the input thread so a deep tree walk
	// (e.g. @~/<rare-name>) can't freeze typing. The worker posts its
	// result back here via uiTaskCh so the editor is mutated and rendered
	// only on the main loop, never from the worker goroutine (which would
	// race the lock-free editor state).
	m.editor.SetAsyncApply(func(apply func()) {
		m.postUITask(func() { apply(); m.tuiInst.RequestRender() })
	})
	m.editor.SetAutocomplete(m.buildAutocompleteProvider())
	m.applyEditorMaxVisible()
	m.statusLine = m.newFooter() // timings rebound after agent constructed
	m.statusLine.SetStatusHook(m.showFlash)
	m.statusLine.SetCwd(m.opts.CWD)
	m.statusLine.SetThinkingLevel(m.settings().DefaultThinkingLevel)
	// Set auto-compact indicator from settings (nil == true default).
	m.statusLine.SetAutoCompactEnabled(m.settings().GetModelCompactionSettings("", "").Enabled)
	// Provider count + subscription indicator for footer.
	m.updateProviderInfo()

	m.statusContainer = tui.NewContainer()
	m.pendingMessagesContainer = tui.NewContainer()
	m.mountInteractiveTui()

	m.slashRegistry = NewSlashRegistry()

	// Load file-based prompt templates from <agentDir>/prompts and
	// <cwd>/.wopr/prompts. They remain fixed for this Session.
	if m.opts.NoPromptTemplates {
		m.promptTemplates = nil
	} else {
		m.loadPromptTemplates()
	}
	if m.opts.ResourceSourceInfoProvider != nil {
		m.resourceSourceInfo = m.opts.ResourceSourceInfoProvider()
	}

	abortCtx, abortFn := context.WithCancel(ctx)
	m.abortCtx = abortCtx
	m.abortFn = abortFn

	// Wire agent + on-disk session: the SDK is the source of truth.
	// SessionHandle MUST be supplied by the caller (cmd/wopr/main.go
	// constructs it via coding.NewSession). This is the F-2 contract
	// post-Chunk F: there is no parallel TUI-only construction path.
	if m.opts.SessionHandle == nil {
		return fmt.Errorf("interactive: SessionHandle is required: construct via coding.NewSession")
	}
	m.agent = m.opts.SessionHandle.Agent()
	m.eventCh = m.opts.SessionHandle.Events()

	// Ignore Kitty key releases for the debug hotkey.
	m.addKeyPressListener(func(data string) bool {
		if !tui.MatchesKeyID(data, "ctrl+shift+d") {
			return false
		}
		m.dispatchSlash("/debug")
		return true
	})

	// Wire steering/follow-up queue modes from settings.
	if sm := m.opts.SettingsManager; sm != nil {
		if mode := sm.GetSteeringMode(); mode != "" {
			m.agent.SetSteeringMode(agent.QueueMode(mode))
		}
		if mode := sm.GetFollowUpMode(); mode != "" {
			m.agent.SetFollowUpMode(agent.QueueMode(mode))
		}
	}

	// Bind the agent's timings recorder to the status line so the
	// elapsed/spinner column reads live from the same source the cost
	// summary does.
	m.statusLine.timings = m.agent.Timings()

	// Set terminal title. Deferred clear restores the shell's own title on
	// exit.
	m.updateTerminalTitle()
	defer tui.SetTerminalTitle("")
	defer tui.SetTerminalProgress(false)
	// Pre-populate status-line name from session on resume.
	if n := m.currentSession().GetSessionName(); n != "" {
		m.statusLine.SetName(n)
	}

	// Initialise thinking level from settings + model capabilities.
	m.initThinkingLevel()

	m.editor.SetAutocomplete(m.buildAutocompleteProvider())

	// Setup opens at launch when asked for, or when nothing could answer a
	// prompt; the home screen is the landing page after it.
	// Not offline (setup needs the network) and not with a model named on
	// the command line.
	firstRun := m.opts.ResumePath == "" && strings.TrimSpace(m.opts.InitialMessage) == "" && len(m.opts.InitialMessages) == 0 &&
		!m.opts.OfflineMode && !m.opts.ModelFromFlag && m.needsFirstRunSetup()
	if m.opts.RunSetup || firstRun {
		m.postUITask(m.runSetup)
	} else if m.opts.NoModelWarning != "" {
		// A runtime warning the user must see to know why no LLM calls will
		// succeed.
		m.showToast("warning", "", m.opts.NoModelWarning)
	}
	m.tuiInst.Render()
	mark("first-render-done")

	// Startup diagnostics show even under quietStartup.
	m.showStartupDiagnostics()
	m.showPromptDiagnostics()
	m.showCrashNotice()

	// Render resumed messages after loaded resources without clearing either.
	if m.opts.ResumePath != "" {
		m.renderSessionEntries()
		m.restoreSidebar()
	}

	// Show "what's new" when the binary version differs from the last
	// recorded version: in full on a fresh session, as a toast pointing to
	// /changelog on a resumed one (a restart after /upgrade).
	if m.opts.SettingsManager != nil && m.opts.AppVersion != "" {
		allEntries := ParseChangelog(wopr.Changelog)
		if newEntries := recordChangelogVersion(m.opts.SettingsManager, m.opts.AppVersion, allEntries); len(newEntries) > 0 && m.opts.ResumePath != "" {
			m.showToastQueued("info", fmt.Sprintf("Updated to %s %s", AppName, m.opts.AppVersion), "Run /changelog to see what's new.")
		} else if len(newEntries) > 0 {
			var body strings.Builder
			for i, newEntrie := range slices.Backward(newEntries) {
				body.WriteString(newEntrie.Content)
				if i > 0 {
					body.WriteString("\n\n")
				}
			}
			m.appendToChat(tui.NewMarkdown(
				"---\n\n**What's New**\n\n" + body.String() + "\n\n---"))
			m.tuiInst.Render()
		}
	}

	m.ensureManagedTools(ctx, tools.NewToolsManager(m.opts.AgentDir))

	// Check tmux keyboard setup asynchronously.
	go func() {
		if warning := checkTmuxKeyboardSetup(); warning != "" {
			// appendChatBlock mutates the render tree and Render reads it;
			// both are owned by the main input loop. Post the work there
			// instead of touching the tree from this goroutine, which races
			// the loop's editor/tree access (invalidatable.dirty and the
			// editor's unsynchronized fields).
			m.postUITask(func() { m.showToastQueued("warning", "", warning) })
		}
	}()

	// Refresh dynamic model catalogs after TUI initialization unless offline,
	// abandoning the refresh after 15 s, then update the provider count.
	if m.opts.Llama != nil && !m.opts.OfflineMode {
		go func() {
			refreshCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			m.opts.Llama.Refresh(refreshCtx, true)
			m.postUITask(m.updateProviderInfo)
		}()
	}

	// Check for a newer wopr binary asynchronously.
	if m.opts.BinaryUpdateChecker != nil {
		go func() {
			update := m.opts.BinaryUpdateChecker()
			if update == nil {
				return
			}
			m.postUITask(func() {
				m.availableUpdate = update.LatestVersion
				m.showToastQueued("info", fmt.Sprintf("%s %s is available", AppName, update.LatestVersion), "Run "+update.Command+" to install it.")
			})
		}()
	}

	// Agent events are drained by the inputLoop select (case <-m.eventCh →
	// handleAgentEvent), so they run on the main goroutine single-threaded with
	// keystrokes and posted UI tasks. No separate event-processor goroutine.

	// Start spinner ticker
	go m.tickSpinner(ctx)

	m.startGitBranchWatcher(ctx)
	m.sidebarMode = m.settings().GetFullscreenSidebar()
	m.startSidebar(ctx)
	defer m.startAgents(ctx)()
	defer m.startGoalAndAsk()()
	m.offerRedispatch()

	// Defensive SIGINT handler: in raw mode the terminal driver
	// shouldn't translate Ctrl+C into a signal (ISIG is off), so the
	// byte handler in inputLoop is the primary abort path. But if
	// something external sends SIGINT (parent process, `kill -INT`,
	// debugger, terminal misconfiguration), route it to the same
	// abort path instead of letting it tear down wopr. SIGTERM is
	// still handled by main.go and exits the whole process.
	// SIGHUP means the terminal is gone (SSH disconnect, window close, etc.).
	// Skip normal TUI cleanup: writing restore sequences to a dead terminal
	// re-triggers EIO/EPIPE. Just kill children and exit 129.
	// Terminal-gone (SIGHUP) handling is unix-only. Windows has no SIGHUP; Go
	// delivers console close/logoff/shutdown as SIGTERM (handled in main.go).
	stopTerminalGone := m.installTerminalGoneHandler(ctx)
	defer stopTerminalGone()

	// An external SIGINT terminates wopr, but the terminal is handed back
	// first so `kill -INT` does not leave the user with ISIG off and a dead
	// Ctrl+C at the shell. Ctrl+C itself never arrives here, because wopr
	// holds the terminal in raw mode and the keymap consumes \x03.
	intCh := make(chan os.Signal, 1)
	signal.Notify(intCh, syscall.SIGINT)
	defer signal.Stop(intCh)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-intCh:
				m.handleInterruptSignal()
			}
		}
	}()

	// Terminal resize re-render. The TUI re-queries term.GetSize on every
	// Render() but renders only fire on input/stream/flash events, so a resize
	// while wopr is idle leaves stale-width rules until the next keystroke. Unix
	// drives this from SIGWINCH; Windows (no SIGWINCH) polls the console size.
	stopResize := m.installResizeHandler(ctx)
	defer stopResize()

	// Auto-submit initial message (positional arg / piped stdin) once the TUI
	// is up. The message is fed through the same handleSubmit path the user's
	// keystrokes hit, so transcript rendering / session writes
	// hooks all see it identically.
	if strings.TrimSpace(m.opts.InitialMessage) != "" || len(m.opts.InitialImages) > 0 {
		m.handleSubmitWithImages(ctx, m.opts.InitialMessage, m.opts.InitialImages)
	}
	if len(m.opts.InitialMessages) > 0 {
		go m.submitInitialMessages(ctx, m.opts.InitialMessages)
	}
	mark("interactive-ready")
	return m.inputLoop(ctx, os.Stdin)
}

func loadThemePath(registry *tui.ThemeRegistry, path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return registry.LoadDir(path)
	}
	theme, err := tui.LoadThemeFile(path)
	if err != nil {
		return err
	}
	registry.Add(theme)
	return nil
}
