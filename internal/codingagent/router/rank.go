package router

import (
	"encoding/json"
	"math"
	"slices"
)

// Ranking orders models strongest first, as "provider/model" specs. The
// order alone is the ranking: there are no ties.
type Ranking []string

// UnmarshalJSON reads a ranking. A ranking written with levels of equally
// strong models ([["a"], ["b", "c"]]) loads as consecutive positions.
func (r *Ranking) UnmarshalJSON(data []byte) error {
	var flat []string
	if err := json.Unmarshal(data, &flat); err == nil {
		*r = flat
		return nil
	}
	var levels [][]string
	if err := json.Unmarshal(data, &levels); err != nil {
		return err
	}
	*r = slices.Concat(levels...)
	return nil
}

// Strength bounds: the strongest model gets rankHigh, the weakest rankLow,
// and the positions between are evenly spaced.
const (
	rankHigh = 0.95
	rankLow  = 0.4
)

// Strengths turns a ranking into the capabilities routing compares against
// demand: evenly spaced by position from 0.95 (strongest) down to 0.4
// (weakest). A single model gets 0.95: the only model is trusted with
// everything, since nothing stronger could take the work.
func Strengths(r Ranking) map[string]float64 {
	out := map[string]float64{}
	n := len(r)
	for i, spec := range r {
		value := rankHigh
		if n > 1 {
			value = rankHigh - (rankHigh-rankLow)*float64(i)/float64(n-1)
		}
		out[spec] = math.Round(value*100) / 100
	}
	return out
}

// RankingFrom orders specs by capability, strongest first, keeping the
// configuration's order among equals: the ranking a configuration written
// before rankings implies.
func RankingFrom(capabilities map[string]float64, order []string) Ranking {
	specs := Ranking(slices.Clone(order))
	slices.SortStableFunc(specs, func(a, b string) int { return cmpFloatDesc(capabilities[a], capabilities[b]) })
	return specs
}
