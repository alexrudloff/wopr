package goal

import (
	"strings"
	"testing"
	"time"
)

func TestParseClaimNeedsAMarkerLine(t *testing.T) {
	for _, tc := range []struct {
		text, kind, evidence string
	}{
		{"Fixed it.\nGOAL MET\n- main.go:3 \"x\"", MetMarker, "- main.go:3 \"x\""},
		{"All tests pass.\n**GOAL MET**", MetMarker, "All tests pass."},
		{"GOAL MET: go test ./... ok", MetMarker, "go test ./... ok"},
		{"I will print GOAL MET when done.", "", ""},
		{"Need the API key.\nGOAL BLOCKED: no credentials", BlockedMarker, "no credentials"},
	} {
		c := ParseClaim(tc.text)
		if c.Kind != tc.kind || c.Evidence != tc.evidence {
			t.Errorf("%q: got %+v", tc.text, c)
		}
	}
}

func TestVerdictPassesOnlyAnAcceptedPass(t *testing.T) {
	if pass, _ := Verdict(true, "PASS\nEVIDENCE (wopr verified 1/1):"); !pass {
		t.Fatal("accepted bare PASS failed")
	}
	if pass, _ := Verdict(true, "PASS: every requirement holds"); !pass {
		t.Fatal("accepted PASS failed")
	}
	if pass, fb := Verdict(false, "PASS looks fine"); pass || !strings.Contains(fb, "could not verify") {
		t.Fatalf("unaccepted PASS: %v %q", pass, fb)
	}
	if pass, fb := Verdict(true, "FAIL: the test still fails"); pass || fb != "FAIL: the test still fails" {
		t.Fatalf("FAIL: %v %q", pass, fb)
	}
}

func TestEachCapPauses(t *testing.T) {
	base := New("g", "x", Caps{Turns: 3, Cost: 1, Time: time.Minute}, time.Now())
	if hit := base.CapHit(); hit != "" {
		t.Fatalf("fresh goal hit %q", hit)
	}
	for name, mutate := range map[string]func(*State){
		"turn":        func(s *State) { s.Turns = 3 },
		"spend":       func(s *State) { s.Spent = 1.2 },
		"time":        func(s *State) { s.Elapsed = 2 * time.Minute },
		"no progress": func(s *State) { s.Idle = IdleTurns },
	} {
		s := base
		mutate(&s)
		if hit := s.CapHit(); !strings.Contains(hit, name) {
			t.Errorf("%s cap: got %q", name, hit)
		}
	}
}

func TestParseArgs(t *testing.T) {
	objective, caps, err := ParseArgs("--turns 5 --cost $2.50 --time 10m make the tests pass")
	if err != nil || objective != "make the tests pass" || caps.Turns != 5 || caps.Cost != 2.5 || caps.Time != 10*time.Minute {
		t.Fatalf("%q %+v %v", objective, caps, err)
	}
	if _, _, err := ParseArgs("--turns 5"); err == nil {
		t.Fatal("a goal without an objective was accepted")
	}
}
