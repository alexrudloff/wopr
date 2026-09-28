package efficiency

import (
	"os"
	"path/filepath"
	"testing"
)

// Learned values stay inside their bounds however one-sided the signals
// are, a damaged file is clamped on load, and a model is never given more
// than its configured window.
func TestLearnedValuesStayInBounds(t *testing.T) {
	check := func(t *testing.T, l *Learner, spec string) {
		t.Helper()
		p := l.PackParams(spec)
		if p.Threshold < packThresholdMin || p.Threshold > packThresholdMax || p.Excerpt < packExcerptMin || p.Excerpt > packExcerptMax {
			t.Fatalf("pack params out of bounds: %+v", p)
		}
		if keep := l.HalfLifeKeep(spec, 32768); keep < halfLifeKeepMin || keep > halfLifeKeepMax {
			t.Fatalf("half-life keep %d out of bounds", keep)
		}
		if n := l.ReducerMinBytes(spec); n < reducerMinBytesMin || n > reducerMinBytesMax {
			t.Fatalf("reducer min bytes %d out of bounds", n)
		}
		if w := l.EffectiveWindow(spec, 32768); w > 32768 || w < int(32768*windowShareMin) {
			t.Fatalf("effective window %d outside [%d, 32768]", w, int(32768*windowShareMin))
		}
	}
	for _, recall := range []bool{true, false} {
		dir := t.TempDir()
		l := LoadLearner(dir, true)
		const spec = "local/model"
		for i := range 400 {
			id := "obs_" + string(rune('a'+i%26)) + string(rune('a'+i/26%26)) + string(rune('a'+i/676))
			l.Placed(spec, id)
			l.Cut(spec, id+"cut")
			l.ReceiptApplied(spec, "/archive/"+id)
			l.Compacted(spec)
			l.ToolCall("read", `{"path":"f.go"}`)
			if recall {
				l.Recalled(id)
				l.Recalled(id + "cut")
				l.ToolCall("bash", `{"command":"sed -n 1,9p /archive/`+id+`"}`)
			}
			l.Stalled(spec, 30000, 32768)
			l.Tick(spec, 30000, 32768)
			check(t, l, spec)
		}
		l.Close()
		check(t, LoadLearner(dir, true), spec)
	}

	dir := t.TempDir()
	damaged := `{"version":1,"models":{"m/x":{"packThreshold":99999999,"packExcerpt":1,"halfLifeKeepDelta":-50,"reducerMinBytes":-4,"windowShare":7}}}`
	if err := os.WriteFile(filepath.Join(dir, LearnedFileName), []byte(damaged), 0o600); err != nil {
		t.Fatal(err)
	}
	check(t, LoadLearner(dir, true), "m/x")
	if err := os.WriteFile(filepath.Join(dir, LearnedFileName), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := LoadLearner(dir, true).EffectiveWindow("m/x", 32768); got != 32768 {
		t.Fatalf("damaged file: effective window %d, want the configured 32768", got)
	}
}
