//go:build windows

package tui

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const consoleReadResultEnv = "WOPR_TUI_CONSOLE_READ_RESULT"

// Undo is bound to Ctrl+Z on Windows, so ReadInput must deliver Ctrl+Z (0x1A)
// from the console as a key. Reading the
// console through os.File.Read reported a Ctrl+Z at the start of a read as end
// of file, which ended wopr's interactive input loop with "error: EOF".
func TestReadInputDeliversCtrlZFromTheConsole(t *testing.T) {
	if result := os.Getenv(consoleReadResultEnv); result != "" {
		readConsoleUntil(t, result, 'b')
		return
	}
	result := filepath.Join(t.TempDir(), "read")
	t.Setenv(consoleReadResultEnv, result)
	console := startInPseudoConsole(t, os.Args[0], "-test.run=^TestReadInputDeliversCtrlZFromTheConsole$", "-test.count=1")
	console.waitForFile(t, result+".ready")
	keys := "a\x1aé\U0001F600\x1ab"
	console.write(t, keys)
	console.wait(t)
	got, err := os.ReadFile(result)
	if err != nil {
		t.Fatalf("read the helper result: %v\nconsole output: %q", err, console.output())
	}
	if string(got) != keys {
		t.Fatalf("ReadInput delivered %q, want %q", got, keys)
	}
}

// keyRecord builds a KEY_EVENT input record.
func keyRecord(down bool, virtualKey, char uint16) inputRecord {
	r := inputRecord{EventType: windows.KEY_EVENT}
	if down {
		binary.LittleEndian.PutUint32(r.Event[0:4], 1)
	}
	binary.LittleEndian.PutUint16(r.Event[4:6], 1)
	binary.LittleEndian.PutUint16(r.Event[6:8], virtualKey)
	binary.LittleEndian.PutUint16(r.Event[10:12], char)
	return r
}

// Console input decoding: UTF-16 surrogates split across reads join into one
// rune (unpaired ones become U+FFFD), and only records ReadConsoleW turns into
// characters count as pending input.
func TestConsoleInputDecoding(t *testing.T) {
	for _, tc := range []struct {
		reads [][]uint16
		want  string
	}{
		{[][]uint16{{'a', 0xD83D}, {0xDE00, 'b'}}, "a\U0001F600b"},
		{[][]uint16{{0xD83D}, {'x'}}, "\uFFFDx"},
		{[][]uint16{{0xDE00, 'y'}}, "\uFFFDy"},
	} {
		var c consoleInput
		var got []byte
		for _, units := range tc.reads {
			got = c.appendUTF8(got, units)
		}
		if string(got) != tc.want {
			t.Errorf("decoded %q, want %q", got, tc.want)
		}
	}
	for _, tc := range []struct {
		name   string
		record inputRecord
		want   bool
	}{
		{"letter pressed", keyRecord(true, 'A', 'a'), true},
		{"letter released", keyRecord(false, 'A', 'a'), false},
		{"arrow pressed", keyRecord(true, windows.VK_UP, 0), true},
		{"shift pressed", keyRecord(true, windows.VK_SHIFT, 0), false},
		{"alt released after keypad code", keyRecord(false, windows.VK_MENU, 0xE9), true},
		{"focus change", inputRecord{EventType: windows.FOCUS_EVENT}, false},
	} {
		if got := tc.record.yieldsCharacter(); got != tc.want {
			t.Errorf("%s: yieldsCharacter = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// readConsoleUntil runs in the child process attached to the pseudo console.
// It reads the console in raw mode through ReadInput until last arrives and
// writes everything it read, or the error that ended the read, to result.
func readConsoleUntil(t *testing.T, result string, last byte) {
	restore, err := EnterRawMode()
	if err != nil {
		t.Fatal(err)
	}
	defer restore()
	if err := os.WriteFile(result+".ready", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var got []byte
	for !bytes.Contains(got, []byte{last}) {
		data, err := ReadInput(os.Stdin)
		if err != nil {
			got = append(got, "error: "+err.Error()...)
			break
		}
		got = append(got, data...)
	}
	if err := os.WriteFile(result, got, 0o600); err != nil {
		t.Fatal(err)
	}
}

type pseudoConsole struct {
	console windows.Handle
	process windows.Handle
	input   *os.File

	mu      sync.Mutex
	out     bytes.Buffer
	drained chan struct{}
}

// startInPseudoConsole starts argv attached to a new pseudo console, as a
// terminal emulator starts a shell, and drains the console's output.
func startInPseudoConsole(t *testing.T, argv ...string) *pseudoConsole {
	t.Helper()
	var inRead, inWrite, outRead, outWrite windows.Handle
	if err := windows.CreatePipe(&inRead, &inWrite, nil, 0); err != nil {
		t.Fatal(err)
	}
	if err := windows.CreatePipe(&outRead, &outWrite, nil, 0); err != nil {
		t.Fatal(err)
	}
	p := &pseudoConsole{
		input:   os.NewFile(uintptr(inWrite), "pseudo-console-input"),
		drained: make(chan struct{}),
	}
	output := os.NewFile(uintptr(outRead), "pseudo-console-output")
	err := windows.CreatePseudoConsole(windows.Coord{X: 100, Y: 30}, inRead, outWrite, 0, &p.console)
	// The pseudo console holds its own references to the ends it uses.
	_ = windows.CloseHandle(inRead)
	_ = windows.CloseHandle(outWrite)
	if err != nil {
		_ = p.input.Close()
		_ = output.Close()
		t.Skipf("pseudo consoles are unavailable: %v", err)
	}
	go func() {
		defer close(p.drained)
		_, _ = io.Copy(p, output)
	}()
	t.Cleanup(func() {
		if p.process != 0 {
			_ = windows.TerminateProcess(p.process, 1)
			_ = windows.CloseHandle(p.process)
		}
		windows.ClosePseudoConsole(p.console)
		_ = p.input.Close()
		<-p.drained
		_ = output.Close()
	})

	attributes, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		t.Fatal(err)
	}
	defer attributes.Delete()
	// The attribute value is the pseudo console handle itself.
	if err := attributes.Update(windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE, *(*unsafe.Pointer)(unsafe.Pointer(&p.console)), unsafe.Sizeof(p.console)); err != nil { //nolint:gosec // G103: UpdateProcThreadAttribute takes the HPCON value in its pointer argument, and x/sys takes that value as unsafe.Pointer.
		t.Fatal(err)
	}
	var startup windows.StartupInfoEx
	startup.Cb = uint32(unsafe.Sizeof(startup))
	// Null standard handles make the child use the pseudo console even when
	// this test's own output is redirected.
	startup.Flags = windows.STARTF_USESTDHANDLES
	startup.ProcThreadAttributeList = attributes.List()
	commandLine, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(argv))
	if err != nil {
		t.Fatal(err)
	}
	var info windows.ProcessInformation
	if err := windows.CreateProcess(nil, commandLine, nil, nil, false, windows.EXTENDED_STARTUPINFO_PRESENT, nil, nil, &startup.StartupInfo, &info); err != nil {
		t.Fatal(err)
	}
	_ = windows.CloseHandle(info.Thread)
	p.process = info.Process
	return p
}

func (p *pseudoConsole) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.out.Write(b)
}

func (p *pseudoConsole) output() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.out.String()
}

func (p *pseudoConsole) write(t *testing.T, keys string) {
	t.Helper()
	if _, err := p.input.WriteString(keys); err != nil {
		t.Fatal(err)
	}
}

func (p *pseudoConsole) waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		if event, _ := windows.WaitForSingleObject(p.process, 0); event == windows.WAIT_OBJECT_0 {
			t.Fatalf("the child exited before %s appeared\nconsole output: %q", path, p.output())
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s did not appear within 30s\nconsole output: %q", path, p.output())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (p *pseudoConsole) wait(t *testing.T) {
	t.Helper()
	event, err := windows.WaitForSingleObject(p.process, 30_000)
	if err != nil {
		t.Fatal(err)
	}
	if event != windows.WAIT_OBJECT_0 {
		t.Fatalf("the child did not exit within 30s\nconsole output: %q", p.output())
	}
	var code uint32
	if err := windows.GetExitCodeProcess(p.process, &code); err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Fatalf("the child exited with %d\nconsole output: %q", code, p.output())
	}
}
