package router

import "testing"

func TestStrengthsFromRanking(t *testing.T) {
	cases := []struct {
		name    string
		ranking Ranking
		want    map[string]float64
	}{
		{"one model", Ranking{"a"}, map[string]float64{"a": 0.95}},
		{"evenly spaced", Ranking{"a", "b", "c"}, map[string]float64{"a": 0.95, "b": 0.68, "c": 0.4}},
		{"five models", Ranking{"a", "b", "c", "d", "e"}, map[string]float64{"a": 0.95, "b": 0.81, "c": 0.68, "d": 0.54, "e": 0.4}},
	}
	for _, tc := range cases {
		got := Strengths(tc.ranking)
		for spec, want := range tc.want {
			if got[spec] != want {
				t.Errorf("%s: %s = %.2f, want %.2f (all: %v)", tc.name, spec, got[spec], want, got)
			}
		}
	}
}
