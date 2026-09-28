package codingagent

import (
	"fmt"
	"io"
	"sync"
	"testing"

	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/tui"
)

// TestConcurrent_SIGWINCHVsTyping probes the reliability question directly:
// the SIGWINCH handler calls applyEditorMaxVisible (writes editor state) and
// Render from its own goroutine, while the main loop mutates the same editor
// on keystrokes. If the editor's fields are unsynchronized, -race flags it.
// Run with: go test -race -run TestConcurrent_SIGWINCHVsTyping
// TestConcurrent_SIGWINCHViaPostUITask_NoRace is the regression guard for the
// fix: routing the resize work through postUITask moves the editor mutation +
// render onto the goroutine that drains uiTaskCh (the main input loop), which
// also owns typing. All editor access then happens on one goroutine, so -race
// stays clean. This is the inverse of TestConcurrent_SIGWINCHVsTyping.
// TestConcurrent_StatusLineInvalidate_NoRace covers the git-branch watcher
// pattern: watchGitBranch calls sl.Invalidate() (and updates gitBranch under
// sl.mu) from its own goroutine while the main loop renders the StatusLine and
// mutates it via Set*. The StatusLine fields are sl.mu-guarded and the dirty
// flag is atomic, so this must be -race clean. Before the atomic change the
// off-loop Invalidate raced the main-loop Invalidate on the dirty bool.
func TestConcurrent_StatusLineInvalidate_NoRace(t *testing.T) {
	sl := NewStatusLine(nil, nil)

	const iters = 4000
	var wg sync.WaitGroup

	// Watcher goroutine: mark dirty + update a mu-guarded field off-loop.
	wg.Go(func() {
		for i := range iters {
			sl.SetName(fmt.Sprintf("branch-%d", i%3))
			sl.Invalidate()
		}
	})

	// Main-loop goroutine: render + mutate.
	wg.Go(func() {
		for i := range iters {
			sl.SetWorking(i%2 == 0)
			_ = sl.Render(80)
			sl.NeedsRedraw()
		}
	})

	wg.Wait()
}

func TestConcurrent_SIGWINCHViaPostUITask_NoRace(t *testing.T) {
	model := &ai.Model{ID: "m", DisplayName: "m", Capabilities: ai.ModelCapabilities{ContextWindow: 8000}}
	m := NewInteractiveMode(InteractiveOptions{CWD: t.TempDir(), Model: model})
	m.editor = tui.NewEditor()
	m.chatContainer = tui.NewContainer()
	m.tuiInst = tui.NewWithOutput(io.Discard, 100, 30, tui.Options{})
	m.tuiInst.Add(m.chatContainer)
	m.tuiInst.Add(m.editor)

	const iters = 2000
	done := make(chan struct{})
	var wg sync.WaitGroup

	// The main input loop: drains posted UI tasks AND handles typing, both
	// on this single goroutine (mirrors inputLoop's select).
	wg.Go(func() {
		typed := 0
		for {
			select {
			case fn := <-m.uiTaskCh:
				fn()
			case <-done:
				return
			default:
				if typed%7 == 0 {
					m.editor.SetText("")
				}
				m.editor.HandleInput("x")
				typed++
			}
		}
	})

	// The SIGWINCH goroutine: posts resize work rather than touching the
	// editor directly (the fixed handler's behavior).
	for range iters {
		m.postUITask(func() {
			m.applyEditorMaxVisible()
			m.tuiInst.Render()
		})
	}
	close(done)
	wg.Wait()
}
