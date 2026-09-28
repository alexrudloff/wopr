package tui

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"regexp"
	"strings"
	"testing"
)

// newAltScreenForTest drops timer-scheduled renders so tests drive frames
// explicitly via Start()/Render().
func newAltScreenForTest(out *bytes.Buffer, width, height int, options Options) *TUI {
	tui := NewWithOutput(out, width, height, options)
	tui.SetRenderDispatcher(func(func()) {})
	return tui
}

// Exiting the alt screen must dump the whole transcript into scrollback, not
// just the visible viewport.
func TestAltScreenLifecycleAndExitDumpsFullTranscript(t *testing.T) {
	var out bytes.Buffer
	tui := newAltScreenForTest(&out, 20, 4, Options{})
	for i := range 30 {
		tui.Add(NewText(fmt.Sprintf("line-%02d", i)))
	}
	tui.Start()
	if !strings.Contains(out.String(), altEnterAltScreen) {
		t.Fatal("Start did not enter the alternate screen")
	}
	out.Reset()
	tui.Stop()
	dumped := out.String()
	if !strings.Contains(dumped, altExitAltScreen) {
		t.Fatal("Stop did not exit the alternate screen")
	}
	for _, want := range []string{"line-00", "line-15", "line-29"} {
		if !strings.Contains(dumped, want) {
			t.Errorf("exit dump missing %q", want)
		}
	}
}

func TestAltScreenScrollFollowAndWheel(t *testing.T) {
	tui := newAltScreenForTest(&bytes.Buffer{}, 20, 4, Options{})
	for range 20 {
		tui.Add(NewText("row"))
	}
	tui.Start()
	top0 := tui.ViewportTop()
	if !tui.IsFollowingOutput() || top0 == 0 {
		t.Fatalf("fresh alt screen should follow output at the end (top %d)", top0)
	}
	if !tui.HandleViewportInput("\x1b[<64;5;5M") || tui.ViewportTop() >= top0 {
		t.Fatalf("wheel-up not consumed or did not scroll up: %d -> %d", top0, tui.ViewportTop())
	}
	if tui.IsFollowingOutput() {
		t.Fatal("scrolled-up view still follows output")
	}
	tui.ScrollToBottom()
	if !tui.IsFollowingOutput() || tui.ViewportTop() != top0 {
		t.Fatal("ScrollToBottom did not resume following output")
	}
}

func TestAltScreenDragSelectionCopiesSelectedText(t *testing.T) {
	var out bytes.Buffer
	tui := newAltScreenForTest(&out, 40, 6, Options{})
	tui.previousScreen = []string{"hello world", "second line"}
	tui.HandleViewportInput("\x1b[<0;1;1M")
	tui.HandleViewportInput("\x1b[<32;5;1M")
	tui.HandleViewportInput("\x1b[<0;5;1m")
	m := regexp.MustCompile(`\x1b\]52;c;([A-Za-z0-9+/=]+)\x07`).FindStringSubmatch(out.String())
	if m == nil {
		t.Fatalf("no OSC 52 clipboard write: %q", out.String())
	}
	if decoded, _ := base64.StdEncoding.DecodeString(m[1]); string(decoded) != "hello" {
		t.Fatalf("clipboard payload = %q, want hello", decoded)
	}
}

func TestAltScreenStopStartClearsInputState(t *testing.T) {
	tui := newAltScreenForTest(&bytes.Buffer{}, 40, 10, Options{})
	for range 20 {
		tui.Add(NewText("row"))
	}
	tui.Start()
	tui.HandleViewportInput("\x1b[<0;5;5M")
	tui.HandleViewportInput("\x1b[<32;6;6M")
	if tui.selectionAnchor == nil {
		t.Fatal("precondition: press+drag should set a selection anchor")
	}
	tui.StopWithOptions(StopOptions{})
	tui.Start()
	tui.mu.Lock()
	defer tui.mu.Unlock()
	if tui.stopped || tui.selectionAnchor != nil || tui.selectionPressActive || tui.selectionDragged || tui.scrollbarDrag != nil {
		t.Fatal("restart left stale stopped/selection/drag state")
	}
}
