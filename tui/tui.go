package tui

// Component model and render scheduling.
// Components implement Render(width int) []string and optionally HandleInput(data string).

import (
	"fmt"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/term"
)

// ─── Component Interface ──────────────────────────────────────────────────────

// Component is the base interface all TUI widgets implement.
// Render returns a slice of ANSI-annotated lines (no trailing newlines).
// Width is the available terminal columns.
type Component interface {
	Render(width int) []string
	// Invalidate marks the component as needing a redraw on the next tick.
	Invalidate()
}

// InputHandler is implemented by components that want keyboard events.
type InputHandler interface {
	HandleInput(data string)
}

// KeyReleaseReceiver is implemented by components that want Kitty key-release
// events delivered to HandleInput. Components that do not implement it get no
// releases; games opt in because they need key-up to stop movement.
type KeyReleaseReceiver interface {
	WantsKeyRelease() bool
}

// Disposable is implemented by components that need cleanup.
type Disposable interface {
	Dispose()
}

// ─── Invalidation ─────────────────────────────────────────────────────────────

// invalidatable is a mixin that provides Invalidate() + NeedsRedraw().
// dirty is atomic because Invalidate is called from background goroutines
// (e.g. the git-branch watcher marks the StatusLine dirty off the main loop),
// concurrent with main-loop Invalidate calls. The flag is embedded by value in
// pointer-only components, so it is never copied after construction.
type invalidatable struct {
	dirty atomic.Bool
}

func (i *invalidatable) Invalidate()       { i.dirty.Store(true) }
func (i *invalidatable) IsDirty() bool     { return i.dirty.Load() }
func (i *invalidatable) NeedsRedraw() bool { return i.dirty.Swap(false) }

// BaseComponent is the exported equivalent of invalidatable for components
// living in other packages.
type BaseComponent struct{ invalidatable }

// ─── Container ────────────────────────────────────────────────────────────────

// Container stacks child components vertically.
type Container struct {
	invalidatable
	children []Component
	mu       sync.RWMutex
	maxLines atomic.Int64 // 0 = unlimited; set via SetMaxLines

	// childCache memoizes each child's rendered lines keyed by component
	// identity. renderedLines is the immutable concatenation consumed by the
	// package renderers; settled frames reuse it without copying Session history.
	// Render clones it so callers own a fresh slice.
	cacheWidth     int
	cacheTheme     *Theme
	childCache     map[Component]cachedChild
	renderLineHint int
	renderedLines  []string
	renderedValid  bool
	renderVersion  uint64

	// mouseLayout records each child's height from the last uncapped render for
	// mouse dispatch. nil after a capped render, which does not render every
	// child.
	mouseLayout *mouseLayout
}

type cachedChild struct {
	width   int
	lines   []string
	version uint64
}

func NewContainer(children ...Component) *Container {
	return &Container{children: children}
}

func (c *Container) invalidateStructureLocked() {
	c.renderedValid = false
	c.dirty.Store(true)
}

// PreviousAware is a component whose rendering depends on the sibling added
// just before it, such as a tool card that stacks tightly under another.
type PreviousAware interface {
	SetPrevious(previous Component)
}

func (c *Container) Add(comp Component) {
	c.mu.Lock()
	var previous Component
	if n := len(c.children); n > 0 {
		previous = c.children[n-1]
	}
	c.children = append(c.children, comp)
	c.invalidateStructureLocked()
	c.mu.Unlock()
	if aware, ok := comp.(PreviousAware); ok {
		aware.SetPrevious(previous)
	}
}

func (c *Container) Remove(comp Component) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, ch := range c.children {
		if ch == comp {
			c.children = append(c.children[:i], c.children[i+1:]...)
			delete(c.childCache, comp)
			c.invalidateStructureLocked()
			return
		}
	}
}

// Replace swaps oldComp with newComp at the same child index.
// Returns true if oldComp was found.
func (c *Container) Replace(oldComp, newComp Component) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, ch := range c.children {
		if ch == oldComp {
			c.children[i] = newComp
			delete(c.childCache, oldComp)
			c.invalidateStructureLocked()
			return true
		}
	}
	return false
}

// Clear removes every child component. Used by /clear and /new.
func (c *Container) Clear() {
	c.mu.Lock()
	c.children = nil
	c.childCache = nil
	c.invalidateStructureLocked()
	c.mu.Unlock()
}

// SetChildren atomically replaces all children.
func (c *Container) SetChildren(children ...Component) {
	c.mu.Lock()
	c.children = append([]Component(nil), children...)
	c.childCache = nil
	c.invalidateStructureLocked()
	c.mu.Unlock()
}

func (c *Container) LastTwoChildren() (Component, Component) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	n := len(c.children)
	if n == 0 {
		return nil, nil
	}
	if n == 1 {
		return nil, c.children[0]
	}
	return c.children[n-2], c.children[n-1]
}

func (c *Container) ChildCount() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.children)
}

// IsEmpty reports whether the container currently has no children.
func (c *Container) IsEmpty() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.children) == 0
}

// SetMaxLines caps how many lines Render() returns. When n>0, only the
// last n lines of all children's output are returned. When n==0 (default),
// all lines are returned. Used by the /tree selector to keep the chat
// scrolled to a minimum context window.
func (c *Container) SetMaxLines(n int) {
	c.maxLines.Store(int64(n))
	c.mu.Lock()
	c.invalidateStructureLocked()
	c.mu.Unlock()
}

// Render returns a fresh slice the caller owns. Package renderers use
// renderBorrowed to read the immutable cached concatenation without copying
// settled transcript history every frame.
func (c *Container) Render(width int) []string {
	return slices.Clone(c.renderBorrowed(width))
}

// renderBorrowed returns immutable lines owned by the container. Callers must
// not edit the slice. A changed child rebuilds the concatenation; an unchanged
// tree at the same width and theme reuses it.
func (c *Container) renderBorrowed(width int) []string {
	lines, _ := c.renderBorrowedVersion(width)
	return lines
}

// renderBorrowedVersion returns the immutable flattened lines and a revision
// that changes whenever those lines are rebuilt. Parent containers use the
// revision to borrow nested container output without trusting a dirty flag that
// an unrelated render may consume.
func (c *Container) renderBorrowedVersion(width int) ([]string, uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	lines := c.renderBorrowedLocked(width)
	return lines, c.renderVersion
}

func (c *Container) renderBorrowedLocked(width int) []string {
	if c.cacheWidth != width || c.cacheTheme != ActiveTheme() {
		c.childCache = nil
		c.cacheWidth = width
		c.cacheTheme = ActiveTheme()
		c.renderedValid = false
	}
	if c.childCache == nil {
		c.childCache = make(map[Component]cachedChild, len(c.children))
	}

	ml := c.maxLines.Load()
	// When capped (e.g. the /tree context window), render only the trailing
	// children needed to fill maxLines instead of the whole history. Rendering
	// every child and discarding all but the last few lines is O(total entries)
	// per frame, which froze the UI on long sessions and made /tree feel hung
	// when keys were held down.
	if ml > 0 {
		var lines []string
		for _, v := range slices.Backward(c.children) {
			child := c.renderChildLocked(v, width)
			// Prepend into a fresh slice so the cached child slice is never
			// mutated by a later append.
			merged := make([]string, 0, capHint(len(child), len(lines)))
			merged = append(merged, child...)
			lines = append(merged, lines...)
			if int64(len(lines)) >= ml {
				break
			}
		}
		if int64(len(lines)) > ml {
			lines = lines[int64(len(lines))-ml:]
		}
		c.mouseLayout = nil
		c.renderVersion++
		return lines
	}

	if c.renderedValid {
		clean := true
		for _, ch := range c.children {
			if c.childNeedsRenderLocked(ch, width) {
				clean = false
				break
			}
		}
		if clean {
			return c.renderedLines
		}
	}

	lines := make([]string, 0, capHint(c.renderLineHint, 64))
	children := make([]mouseChild, len(c.children))
	for i, ch := range c.children {
		childLines := c.renderChildLocked(ch, width)
		children[i] = mouseChild{component: ch, height: len(childLines)}
		lines = append(lines, childLines...)
	}
	c.mouseLayout = &mouseLayout{width: width, children: children}
	c.renderLineHint = len(lines)
	c.renderedLines = lines
	c.renderedValid = true
	c.renderVersion++
	return c.renderedLines
}

// childNeedsRenderLocked reports whether retrieving ch would call Render.
// c.mu must be held.
func (c *Container) childNeedsRenderLocked(ch Component, width int) bool {
	if nested, ok := ch.(*Container); ok {
		_, version := nested.renderBorrowedVersion(width)
		e, hit := c.childCache[ch]
		return !hit || e.width != width || e.version != version
	}
	dc, ok := ch.(dirtyComponent)
	if !ok {
		return true
	}
	e, hit := c.childCache[ch]
	return !hit || e.width != width || dc.IsDirty()
}

// renderChildLocked returns a child's rendered lines, reusing the cached
// result when the child is clean at the same width. Exact nested containers
// publish an immutable revision, so their lines can be borrowed recursively.
// c.mu must be held.
func (c *Container) renderChildLocked(ch Component, width int) []string {
	if nested, ok := ch.(*Container); ok {
		lines, version := nested.renderBorrowedVersion(width)
		c.childCache[ch] = cachedChild{width: width, lines: lines, version: version}
		return lines
	}
	dc, ok := ch.(dirtyComponent)
	if !ok {
		return ch.Render(width)
	}
	if e, hit := c.childCache[ch]; hit && e.width == width && !dc.IsDirty() {
		return e.lines
	}
	lines := ch.Render(width)
	// Consume the dirty flag so an unchanged child hits the cache next frame.
	dc.NeedsRedraw()
	c.childCache[ch] = cachedChild{width: width, lines: lines}
	return lines
}

// dirtyComponent is a Component that reports whether its rendered output
// changed since the last consume. Components live-refreshing from the wall
// clock (running bash elapsed, animated loaders) must report IsDirty()==true
// while live so the per-child cache does not freeze them.
type dirtyComponent interface {
	IsDirty() bool
	NeedsRedraw() bool
}

type stoppableTimer interface {
	Stop() bool
}

const minRenderInterval = 16 * time.Millisecond

// ShowHardwareCursor reports whether the real terminal cursor is shown.
func (t *TUI) ShowHardwareCursor() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.showHardwareCursor
}

func (t *TUI) updateSize() {
	if t.fixedSize {
		return
	}
	w, h, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil {
		w, h = 80, 24
	}
	t.width = w
	t.height = h
}

// SetFixedSize changes the size of a renderer built with a fixed size
// (NewWithOutput), as a terminal resize would; the next render sees it.
// Renderers that read the real terminal size ignore it.
func (t *TUI) SetFixedSize(cols, rows int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.fixedSize {
		t.width, t.height = cols, rows
	}
}

// Width returns the current terminal width.
func (t *TUI) Width() int { return t.width }

// SetRenderDispatcher installs a hook that runs throttled scheduled renders
// on the caller's main loop. The dispatcher receives a render closure and is
// responsible for eventually invoking it on the goroutine that owns
// component-tree mutation (it may enqueue it on an event loop). When nil
// (default), scheduled renders run inline on the throttle-timer goroutine,
// which is correct for standalone / single-goroutine use. Thread-safe.
func (t *TUI) SetRenderDispatcher(dispatch func(render func())) {
	t.mu.Lock()
	t.renderOnMain = dispatch
	t.mu.Unlock()
}

// SetTickDispatcher installs the blocking owner-loop seam for owned
// state-machine ticks (alt-screen selection auto-scroll). It must marshal fn
// onto the loop that owns rendering, backpressuring rather than dropping while
// that loop is alive; see the tickOnMain doc. When unset the tick runs inline.
// Thread-safe.
func (t *TUI) SetTickDispatcher(dispatch func(func())) {
	t.mu.Lock()
	t.tickOnMain = dispatch
	t.mu.Unlock()
}

// Height returns the current terminal height.
func (t *TUI) Height() int { return t.height }

// RequestRender asks the TUI to render soon, coalescing repeated calls and
// enforcing the 16ms frame throttle. Use this for hot streaming paths
// (thinking/text deltas); direct Render() is reserved for low-frequency state
// changes and final flushes.
func (t *TUI) RequestRender() {
	if t.stopped {
		return
	}
	t.requestRender(false)
}

// requestRender coalesces repeated render requests and enforces a minimum
// delay between frames.
func (t *TUI) requestRender(force bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if force {
		t.forceRedraw = true
		t.renderGeneration++
		if t.renderTimer != nil {
			t.renderTimer.Stop()
			t.renderTimer = nil
		}
		t.renderRequested = true
		generation := t.renderGeneration
		t.renderTimer = t.afterFunc(0, func() { t.runScheduledRender(generation) })
		return
	}
	if t.renderRequested {
		return
	}
	t.renderRequested = true
	t.scheduleRenderLocked()
}

func (t *TUI) scheduleRenderLocked() {
	if t.renderTimer != nil || !t.renderRequested {
		return
	}
	delay := time.Duration(0)
	if !t.lastRenderAt.IsZero() {
		elapsed := t.now().Sub(t.lastRenderAt)
		if elapsed < minRenderInterval {
			delay = minRenderInterval - elapsed
		}
	}
	generation := t.renderGeneration
	t.renderTimer = t.afterFunc(delay, func() { t.runScheduledRender(generation) })
}

func (t *TUI) runScheduledRender(generation uint64) {
	t.mu.Lock()
	if generation != t.renderGeneration {
		t.mu.Unlock()
		return
	}
	t.renderTimer = nil
	if !t.renderRequested {
		t.mu.Unlock()
		return
	}
	t.renderRequested = false
	dispatch := t.renderOnMain
	t.mu.Unlock()

	// The throttle delay ran on a timer goroutine, but the render itself
	// must run on the owner's main loop so doRender never reads the
	// lock-free component tree concurrently with the main loop's mutations.
	if dispatch != nil {
		dispatch(func() { t.renderScheduled(generation) })
		return
	}
	t.renderScheduled(generation)
}

func (t *TUI) renderScheduled(generation uint64) {
	t.mu.Lock()
	current := generation == t.renderGeneration
	t.mu.Unlock()
	if !current {
		return
	}
	t.renderAndReschedule()
}

// renderAndReschedule performs the actual render and schedules a follow-up
// frame if more render requests arrived. It must run on whichever goroutine
// owns component-tree mutation (the main loop when renderOnMain is set).
func (t *TUI) renderAndReschedule() {
	t.doRender()

	t.mu.Lock()
	t.scheduleRenderLocked()
	t.mu.Unlock()
}

// CancelPendingRender invalidates a throttled frame, including one whose timer
// callback has already handed it to the owner loop. It maps the single-threaded
// JavaScript event-loop rule that a later state transition can consume a queued
// request before its callback runs.
func (t *TUI) CancelPendingRender() {
	t.mu.Lock()
	t.renderGeneration++
	t.renderRequested = false
	if t.renderTimer != nil {
		t.renderTimer.Stop()
		t.renderTimer = nil
	}
	t.mu.Unlock()
}

// Render paints a frame now, dropping any pending throttled one.
func (t *TUI) Render() {
	if t.stopped {
		return
	}
	t.CancelPendingRender()
	t.doRender()
}

// HideCursor hides the terminal cursor.
func (t *TUI) HideCursor() {
	_, _ = fmt.Fprint(t.out, "\033[?25l")
}

// ShowCursor shows the terminal cursor.
func (t *TUI) ShowCursor() {
	_, _ = fmt.Fprint(t.out, "\033[?25h")
}

// detectKitty checks if the terminal supports the Kitty graphics protocol.
func detectKitty() bool {
	term := os.Getenv("TERM")
	termProg := os.Getenv("TERM_PROGRAM")
	return term == "xterm-kitty" || termProg == "ghostty" || termProg == "kitty"
}
