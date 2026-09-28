package ai

import "testing"

func TestAdjustMaxTokensForThinking(t *testing.T) {
	cases := []struct {
		name                  string
		base, model           int
		level                 string
		wantMax, wantThinking int
	}{
		{"base preserved without legacy 32k cap", 64000, 128000, "off", 64000, 0},
		{"off", 32000, 128000, "off", 32000, 0},
		{"medium level", 32000, 128000, "medium", 40192, 8192},
		{"high level", 32000, 128000, "high", 48384, 16384},
		{"xhigh clamped to high", 32000, 128000, "xhigh", 48384, 16384},
		{"small model caps both", 4000, 8000, "high", 8000, 6976},
		{"very small model", 512, 1500, "high", 1500, 476},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotMax, gotThinking := AdjustMaxTokensForThinking(&tc.base, tc.model, tc.level, nil)
			if gotMax != tc.wantMax {
				t.Errorf("maxTokens = %d, want %d", gotMax, tc.wantMax)
			}
			if gotThinking != tc.wantThinking {
				t.Errorf("thinkingBudget = %d, want %d", gotThinking, tc.wantThinking)
			}
		})
	}
}
