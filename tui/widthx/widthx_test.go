package widthx

import (
	"strings"
	"testing"
)

func TestVisibleWidth(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"hello", 5},
		{"a\tb", 5},
		{"\x1b[1;31mhello\x1b[0m", 5},
		{"\x1b]8;;https://e.com\x07link\x1b]8;;\x07", 4},
		{"a\x1b_wopr:c\x07b", 2},
		{"a你b好", 6},
		{"👨‍👩‍👧‍👦", 2},
		{"กำ", 2},
	}
	for _, tc := range cases {
		if got := VisibleWidth(tc.in); got != tc.want {
			t.Errorf("VisibleWidth(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestTruncateToWidthFitsAndKeepsStyle(t *testing.T) {
	got := TruncateToWidth("\x1b[31mhello world\x1b[0m", 8, "...", false)
	if !strings.Contains(got, "\x1b[31m") || !strings.Contains(got, "...") || VisibleWidth(got) != 8 {
		t.Fatalf("styled truncate = %q (width %d)", got, VisibleWidth(got))
	}
	for _, w := range []int{1, 2, 6} {
		if got := TruncateToWidth("你好世界", w, "...", false); VisibleWidth(got) > w {
			t.Errorf("TruncateToWidth(CJK, %d) = %q, width %d", w, got, VisibleWidth(got))
		}
	}
}

// Wrapped rows fit the width, and styles/hyperlinks are closed at each row end
// and reopened on the next so they never bleed into caller padding.
func TestWrapTextWithAnsiFitsAndClosesStyles(t *testing.T) {
	for _, line := range WrapTextWithAnsi("日本語テスト hello world 你好世界 "+strings.Repeat("x", 50), 7) {
		if w := VisibleWidth(line); w > 7 {
			t.Errorf("wrapped row %q width %d > 7", line, w)
		}
	}
	got := WrapTextWithAnsi("\x1b]8;;https://example.com\x1b\\look at this long link target\x1b]8;;\x1b\\", 10)
	if len(got) < 2 || !strings.Contains(got[0], "\x1b]8;;\x1b\\") || !strings.Contains(got[1], "\x1b]8;;https://example.com") {
		t.Fatalf("hyperlink not closed/reopened across wrap: %#v", got)
	}
	got = WrapTextWithAnsi("\x1b[4munderlined wraps\x1b[0m", 11)
	if len(got) != 2 || !strings.HasSuffix(got[0], "\x1b[24m") || !strings.HasPrefix(got[1], "\x1b[4m") {
		t.Fatalf("underline not closed/reopened across wrap: %#v", got)
	}
}
