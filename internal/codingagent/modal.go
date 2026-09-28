package codingagent

import "github.com/alexrudloff/wopr/tui"

// modal is a component that takes over keyboard input until done reports
// true.
type modal struct {
	component   tui.Component
	handleInput func(string)
	done        func() bool
	// wake repaints; each function on tasks runs on the loop, then repaints.
	wake  <-chan struct{}
	tasks <-chan func()
	// stop ends the loop early.
	stop <-chan struct{}
}

// modalComponent is the usual shape of a modal: a component that handles
// its own input and reports when it is done.
type modalComponent interface {
	tui.Component
	HandleInput(string)
	Done() bool
}

func modalOf(c modalComponent) modal {
	return modal{component: c, handleInput: c.HandleInput, done: c.Done}
}

// runInSlot shows md's component in place of the editor, below the
// transcript, and feeds it input until done. It returns false when stop
// closes or input ends first.
func (m *InteractiveMode) runInSlot(md modal) bool {
	if m.setup != nil {
		// The setup screen has no editor; its panel hosts the modal.
		return m.runSetupModal(md)
	}
	m.editorContainer.SetChildren(md.component)
	defer func() {
		m.editorContainer.SetChildren(m.editor)
		m.tuiInst.RequestRender()
	}()
	return m.feedModal(md, false)
}

// feedModal renders and feeds md input until done. A burst of queued
// keystrokes (key-repeat, paste) is applied before one repaint. With mouse
// set, mouse reports go through the renderer, which maps them to the
// component's rows.
func (m *InteractiveMode) feedModal(md modal, mouse bool) bool {
	inputCh, releaseInput := m.acquireModalInputChannel()
	defer releaseInput()
	feed := func(buf []byte) {
		for _, chunk := range dropKeyReleases(md.component, []string{string(buf)}) {
			if mouse && tui.IsMouseSequence(chunk) {
				m.tuiInst.HandleViewportInput(chunk)
			} else {
				md.handleInput(chunk)
			}
			if md.done() {
				return
			}
		}
	}
	m.tuiInst.Render()
	for !md.done() {
		select {
		case buf, ok := <-inputCh:
			if !ok {
				return false
			}
			feed(buf)
		drain:
			for !md.done() {
				select {
				case more := <-inputCh:
					feed(more)
				default:
					break drain
				}
			}
		case <-md.wake:
		case task := <-md.tasks:
			task()
		case <-md.stop:
			return false
		}
		m.tuiInst.Render()
	}
	return true
}
