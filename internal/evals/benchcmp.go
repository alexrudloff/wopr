package evals

import (
	"bufio"
	"fmt"
	"io"
	"maps"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

var benchLineRE = regexp.MustCompile(`^(Benchmark\S+?)(?:-\d+)?\s+\d+\s+(.*)$`)

// ParseBench reads `go test -bench` output into benchmark -> unit -> values.
func ParseBench(r io.Reader) map[string]map[string][]float64 {
	results := map[string]map[string][]float64{}
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		m := benchLineRE.FindStringSubmatch(strings.TrimSpace(scanner.Text()))
		if m == nil {
			continue
		}
		fields := strings.Fields(m[2])
		units := results[m[1]]
		if units == nil {
			units = map[string][]float64{}
			results[m[1]] = units
		}
		for i := 0; i+1 < len(fields); i += 2 {
			if v, err := strconv.ParseFloat(fields[i], 64); err == nil {
				units[fields[i+1]] = append(units[fields[i+1]], v)
			}
		}
	}
	return results
}

// BenchCompare compares two benchmark files by median, writes a table to w,
// and returns the number of time or allocation regressions above threshold
// percent.
func BenchCompare(oldPath, newPath string, threshold float64, w io.Writer) (int, error) {
	parse := func(path string) (map[string]map[string][]float64, error) {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer func() { _ = f.Close() }()
		return ParseBench(f), nil
	}
	old, err := parse(oldPath)
	if err != nil {
		return 0, err
	}
	cur, err := parse(newPath)
	if err != nil {
		return 0, err
	}
	regressions := 0
	_, _ = fmt.Fprintf(w, "%-48s %-10s %12s %12s %8s\n", "benchmark", "unit", "old", "new", "delta")
	var only []string
	for _, name := range slices.Sorted(maps.Keys(old)) {
		if _, ok := cur[name]; !ok {
			only = append(only, name)
			continue
		}
		for _, unit := range slices.Sorted(maps.Keys(old[name])) {
			values, ok := cur[name][unit]
			if !ok {
				continue
			}
			a, b := median(old[name][unit]), median(values)
			delta := 0.0
			if a != 0 {
				delta = (b - a) / a * 100
			}
			mark := ""
			if (unit == "ns/op" || unit == "B/op" || unit == "allocs/op") && delta > threshold {
				regressions++
				mark = "  REGRESSION"
			}
			_, _ = fmt.Fprintf(w, "%-48s %-10s %12.1f %12.1f %+7.1f%%%s\n", truncate(name, 48), unit, a, b, delta, mark)
		}
	}
	for name := range cur {
		if _, ok := old[name]; !ok {
			only = append(only, name)
		}
	}
	if len(only) > 0 {
		slices.Sort(only)
		_, _ = fmt.Fprintf(w, "benchcmp: present in one run only: %s\n", strings.Join(only, ", "))
	}
	_, _ = fmt.Fprintf(w, "benchcmp: %d regression(s) above %g%% (medians; use -count=6 or more for stable medians)\n", regressions, threshold)
	return regressions, nil
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
