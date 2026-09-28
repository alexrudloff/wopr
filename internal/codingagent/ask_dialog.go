package codingagent

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/alexrudloff/wopr/internal/codingagent/askuser"
	"github.com/alexrudloff/wopr/tui"
	"github.com/alexrudloff/wopr/tui/widthx"
)

// ask_user in the TUI: a question dialog with the model's options and a
// free-text row. A question that arrives while the user is typing waits,
// with a toast, until the editor is empty or the user opens it.

// questionKey is the leader key that opens a waiting question.
const questionKey = "?"

// askPadX is the dialog's inset, as in the other dialogs.
const askPadX = 4

// askDialog is the question dialog: the question, numbered options, and a
// last row that turns into a text field for the user's own answer.
type askDialog struct {
	tui.BaseComponent
	q        askuser.Question
	selected int
	typing   bool
	input    *tui.TextInput
	done     bool
	answer   askuser.Answer
	// rowOf maps a rendered line to its option index (len(q.Options) is
	// the free-text row), or -1.
	rowOf []int
}

func newAskDialog(q askuser.Question) *askDialog {
	prompt := ""
	return &askDialog{q: q, input: tui.NewInput(tui.InputOptions{Prompt: &prompt, Placeholder: "Type your answer"})}
}

// otherRow is the index of the free-text row.
func (d *askDialog) otherRow() int { return len(d.q.Options) }

// choose answers with row i, or starts typing on the free-text row.
func (d *askDialog) choose(i int) {
	d.selected = i
	if i == d.otherRow() {
		d.typing = true
		return
	}
	d.answer, d.done = askuser.Answer{Choice: d.q.Options[i].Label}, true
}

func (d *askDialog) HandleInput(data string) {
	defer d.Invalidate()
	if d.typing {
		switch {
		case tui.MatchesKeyID(data, "escape"):
			d.typing = false
		case tui.MatchesKeyID(data, "enter"):
			if text := strings.TrimSpace(d.input.Text()); text != "" {
				d.answer, d.done = askuser.Answer{Other: text}, true
			}
		default:
			d.input.HandleInput(data)
		}
		return
	}
	n := d.otherRow() + 1
	switch {
	case tui.MatchesKeyID(data, "escape"), tui.MatchesKeyID(data, "ctrl+c"):
		d.answer, d.done = askuser.Answer{Declined: true}, true
	case tui.MatchesKeyID(data, "up"), tui.MatchesKeyID(data, "ctrl+p"):
		d.selected = (d.selected + n - 1) % n
	case tui.MatchesKeyID(data, "down"), tui.MatchesKeyID(data, "ctrl+n"), tui.MatchesKeyID(data, "tab"):
		d.selected = (d.selected + 1) % n
	case tui.MatchesKeyID(data, "enter"):
		d.choose(d.selected)
	case len(data) == 1 && data[0] >= '1' && int(data[0]-'0') <= n:
		d.choose(int(data[0] - '1'))
	}
}

// HandleMouse picks the clicked row.
func (d *askDialog) HandleMouse(event tui.TuiMouseEvent) *tui.TuiMouseDispatchResult {
	if event.Button != tui.MouseButtonLeft || (event.Type != tui.MousePress && event.Type != tui.MouseClick) ||
		event.Y < 0 || event.Y >= len(d.rowOf) || d.rowOf[event.Y] < 0 {
		return nil
	}
	i := d.rowOf[event.Y]
	if event.Type == tui.MousePress {
		d.selected = i
	} else if !d.typing || i != d.otherRow() {
		d.typing = false
		d.choose(i)
	}
	d.Invalidate()
	return &tui.TuiMouseDispatchResult{Handled: true}
}

func (d *askDialog) Render(width int) []string {
	th := tui.ActiveTheme()
	pad := strings.Repeat(" ", askPadX)
	inner := max(1, width-2*askPadX)
	title := bold(th.FgText("text", "Question"))
	out := []string{pad + spread(title, th.FgText("textMuted", "esc"), inner), ""}
	for _, line := range widthx.WrapTextWithAnsi(d.q.Question, inner) {
		out = append(out, pad+th.FgText("text", line))
	}
	out = append(out, "")
	rows := slices.Repeat([]int{-1}, len(out))
	rowWidth := max(1, width-2)
	for i := 0; i <= d.otherRow(); i++ {
		label, detail := "Type your own answer", ""
		if i < d.otherRow() {
			label, detail = d.q.Options[i].Label, d.q.Options[i].Description
		}
		active := i == d.selected
		text, muted := "text", "textMuted"
		if active {
			text, muted = "selectedListItemText", "selectedListItemText"
		}
		body := fmt.Sprintf("%d. ", i+1) + label
		if active {
			body = bold(th.FgText(text, body))
		} else {
			body = th.FgText(text, body)
		}
		row := "   " + body
		if active {
			row = " " + tui.FillBackground(row, rowWidth, th.Bg("primary"))
		} else {
			row = " " + row
		}
		out, rows = append(out, widthx.TruncateToWidth(row, width, "…", false)), append(rows, i)
		if detail != "" {
			for _, line := range widthx.WrapTextWithAnsi(detail, max(1, inner-3)) {
				out, rows = append(out, pad+"   "+th.FgText(muted, line)), append(rows, i)
			}
		}
		if i == d.otherRow() && d.typing {
			d.input.Focused = true
			field := d.input.Render(max(1, inner-3))
			if len(field) > 0 {
				out, rows = append(out, pad+"   "+field[0]), append(rows, i)
			}
		}
	}
	hint := "↑↓ select · enter choose · 1-" + fmt.Sprint(d.otherRow()+1) + " pick · esc skip"
	if d.typing {
		hint = "enter send · esc back"
	}
	out = append(out, "", pad+th.FgText("textMuted", hint), "")
	d.rowOf = slices.Concat(rows, []int{-1, -1, -1})
	return out
}

// pendingQuestion is a question waiting for the user.
type pendingQuestion struct {
	ctx    context.Context
	q      askuser.Question
	result chan askuser.Answer
}

// askUser is the session's asker: it shows the question on the owner loop
// and waits for the answer or for the run to end.
func (m *InteractiveMode) askUser(ctx context.Context, q askuser.Question) (askuser.Answer, error) {
	pq := &pendingQuestion{ctx: ctx, q: q, result: make(chan askuser.Answer, 1)}
	if err := m.postToMain(ctx, func() { m.offerQuestion(pq) }); err != nil {
		return askuser.Answer{}, err
	}
	select {
	case a := <-pq.result:
		return a, nil
	case <-ctx.Done():
		return askuser.Answer{}, ctx.Err()
	}
}

// offerQuestion opens the question, or keeps it waiting behind a toast
// while the user is typing or another dialog is open.
func (m *InteractiveMode) offerQuestion(pq *pendingQuestion) {
	if strings.TrimSpace(m.editor.Text()) != "" || m.dialogDepth > 0 {
		m.pendingQuestion = pq
		m.showToast("info", "", "Question waiting · "+leaderKey+" "+questionKey)
		return
	}
	m.openQuestion(pq)
}

// openPendingQuestion opens the waiting question, if any.
func (m *InteractiveMode) openPendingQuestion() {
	pq := m.pendingQuestion
	m.pendingQuestion = nil
	if pq != nil {
		m.openQuestion(pq)
	}
}

// openQuestion runs the dialog and hands the answer back; a run that ends
// first closes it.
func (m *InteractiveMode) openQuestion(pq *pendingQuestion) {
	if pq.ctx.Err() != nil {
		return
	}
	if m.toastHandle != nil {
		m.toastHandle.Close()
		m.toastHandle = nil
	}
	d := newAskDialog(pq.q)
	if !m.runDialog(modal{component: d, handleInput: d.HandleInput, done: func() bool { return d.done }, stop: pq.ctx.Done()}, dialogMedium) {
		return
	}
	pq.result <- d.answer
}

// onEditorChange opens a waiting question, or resumes the goal, once the
// editor is empty. It runs inside the editor's input handling, so the
// work is posted.
func (m *InteractiveMode) onEditorChange(text string) {
	if text != "" {
		return
	}
	m.postUITask(func() {
		if m.editor.Text() != "" {
			return
		}
		if m.pendingQuestion != nil {
			m.openPendingQuestion()
			return
		}
		m.maybeContinueGoal()
	})
}
