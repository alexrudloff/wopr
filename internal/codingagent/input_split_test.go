package codingagent

import (
	"testing"

	"github.com/alexrudloff/wopr/tui"
)

func TestStdinBufferDispatch_DropsKittyKeyRelease(t *testing.T) {
	// Under the Kitty keyboard protocol (extendedKeyInit pushes \x1b[>7u, whose
	// flag 2 reports event types), a keypress emits a press event AND a release
	// event (event type :3). Releases must not reach a focused component or
	// every key fires twice: the settings-menu up-arrow moved twice per press,
	// `/` was inserted twice.
	//
	// StdinBuffer decodes terminal reads before focus routing; the delivery
	// filter then applies the KeyReleaseReceiver rule per sequence.
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"up-arrow press kept, release dropped", "\x1b[1;1:1A\x1b[1;1:3A", []string{"\x1b[1;1:1A"}},
		{"slash press kept, release dropped", "/\x1b[47;1:3u", []string{"/"}},
		{"bare arrow release dropped", "\x1b[1;1:3A", nil},
		{"bare CSI-u release dropped", "\x1b[47;1:3u", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var b StdinBuffer
			got := dropKeyReleases(tui.NewSlotInputComponent("t", "p"), b.ProcessString(tc.in))
			if len(got) != len(tc.want) {
				t.Fatalf("delivery of StdinBuffer input %q =\n  got  %#v\n  want %#v", tc.in, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("StdinBuffer input %q[%d] = %q, want %q", tc.in, i, got[i], tc.want[i])
				}
			}
		})
	}
}
