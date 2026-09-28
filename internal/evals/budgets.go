package evals

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"slices"

	"github.com/BurntSushi/toml"
)

// Budgets maps an overhead scenario to metric ceilings, from the
// [overhead.<scenario>] tables of budgets.toml.
type Budgets map[string]map[string]float64

// LoadBudgets reads the [overhead] tables of a budgets file. Other tables
// (such as [slop]) belong to other gates and are ignored.
func LoadBudgets(path string) (Budgets, error) {
	var file struct {
		Overhead Budgets `toml:"overhead"`
	}
	if _, err := toml.DecodeFile(path, &file); err != nil {
		return nil, err
	}
	return file.Overhead, nil
}

// CheckBudgets applies budgets to report, writes one line per ceiling to
// w, and returns the number of ceilings missed. A metric the report lacks
// counts as missed.
func CheckBudgets(report *OverheadReport, budgets Budgets, w io.Writer) int {
	failures := 0
	for _, scenario := range slices.Sorted(maps.Keys(budgets)) {
		limits := budgets[scenario]
		values := map[string]float64{}
		if r := report.Result(scenario); r != nil && r.Runs > 0 {
			data, _ := json.Marshal(r)
			_ = json.Unmarshal(data, &values)
		}
		for _, metric := range slices.Sorted(maps.Keys(limits)) {
			ceiling := limits[metric]
			value, ok := values[metric]
			status := "ok  "
			if !ok || value > ceiling {
				status = "FAIL"
				failures++
			}
			shown := "missing"
			if ok {
				shown = fmt.Sprint(value)
			}
			_, _ = fmt.Fprintf(w, "budget: %s %s %s = %s (ceiling %v)\n", status, scenario, metric, shown, ceiling)
		}
	}
	return failures
}
