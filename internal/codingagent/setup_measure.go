package codingagent

import (
	"cmp"
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/internal/codingagent/router"
)

// Measuring runs by itself, in the background, after a model is added (and
// again on request): a few tiny requests to learn whether it calls tools,
// streams, which thinking levels it takes, and how fast it answers, so
// routing can pick the fastest model that is strong enough. Results land in
// router-speed.json and, for an endpoint model, in its models.json entry;
// setup's rows and the sidebar show progress.

// measureWhy is the sentence that tells people what measuring is for.
const measureWhy = "wopr measures each model's speed by itself, with a few tiny requests."

// measureStatus is one model's measuring state.
type measureStatus struct {
	running bool
	step    string
	// summary is the plain-words result, or the error.
	summary string
	failed  bool
}

// modelMeasurer runs one measurement at a time and keeps each model's
// status for setup and the sidebar. It is safe for concurrent use.
type modelMeasurer struct {
	mu      sync.Mutex
	status  map[string]*measureStatus
	names   map[string]string
	version int
	// run serializes measurements, so they never compete for a server.
	run sync.Mutex
}

// setupFilesMu serializes the config writes of setup and of background
// measuring.
var setupFilesMu sync.Mutex

func (m *InteractiveMode) measurer() *modelMeasurer {
	m.measureOnce.Do(func() {
		m.measure = &modelMeasurer{status: map[string]*measureStatus{}, names: map[string]string{}}
	})
	return m.measure
}

func (mm *modelMeasurer) set(spec string, update func(*measureStatus)) {
	mm.mu.Lock()
	defer mm.mu.Unlock()
	st := mm.status[spec]
	if st == nil {
		st = &measureStatus{}
		mm.status[spec] = st
	}
	update(st)
	mm.version++
}

// get returns spec's status, if it was measured in this run.
func (mm *modelMeasurer) get(spec string) (measureStatus, bool) {
	mm.mu.Lock()
	defer mm.mu.Unlock()
	st, ok := mm.status[spec]
	if !ok {
		return measureStatus{}, false
	}
	return *st, true
}

// active names the model being measured, or "".
func (mm *modelMeasurer) active() string {
	mm.mu.Lock()
	defer mm.mu.Unlock()
	for spec, st := range mm.status {
		if st.running {
			return mm.names[spec]
		}
	}
	return ""
}

func (mm *modelMeasurer) changes() int {
	mm.mu.Lock()
	defer mm.mu.Unlock()
	return mm.version
}

// measureModel starts measuring s in the background.
func (m *InteractiveMode) measureModel(s *setupModel) {
	mm := m.measurer()
	spec := s.spec()
	name := cmp.Or(s.Name, s.Model)
	mm.mu.Lock()
	mm.names[spec] = name
	mm.mu.Unlock()
	mm.set(spec, func(st *measureStatus) { *st = measureStatus{running: true, step: "waiting"} })
	model := *s
	go func() {
		mm.run.Lock()
		defer mm.run.Unlock()
		ctx := m.runCtxOrBackground()
		var result modelMeasure
		if model.Endpoint != nil {
			ep := *model.Endpoint
			if ep.APIKey == "" {
				ep.APIKey = m.storedKey(model.Provider)
			}
			result = probeEndpointModel(ctx, ep, model.Model, func(step string) {
				mm.set(spec, func(st *measureStatus) { st.step = step })
			})
		} else {
			mm.set(spec, func(st *measureStatus) { st.step = "one tiny request" })
			result = m.probeCatalog(ctx, spec)
		}
		m.applyMeasure(&model, &result)
		mm.set(spec, func(st *measureStatus) {
			st.running, st.step = false, ""
			st.summary, st.failed = measureWords(&result), !result.ok()
		})
		m.postUITask(m.reloadRouterQuietly)
	}()
}

// storedKey is a provider's stored API key, or "".
func (m *InteractiveMode) storedKey(provider string) string {
	auth, err := ai.NewAuthStorage(filepath.Join(m.opts.AgentDir, "auth.json"))
	if err != nil {
		return ""
	}
	key, ok, err := ai.ResolveStoredAPIKeyFromStorage(m.runCtxOrBackground(), auth, provider)
	if err != nil || !ok {
		return ""
	}
	return key
}

func (m *InteractiveMode) probeCatalog(ctx context.Context, spec string) modelMeasure {
	if m.opts.ModelBuilder == nil {
		return modelMeasure{Err: "no model builder"}
	}
	model, err := m.opts.ModelBuilder(spec)
	if err != nil {
		return modelMeasure{Err: err.Error()}
	}
	return probeCatalogModel(ctx, model)
}

// applyMeasure writes what a measurement learned: the speed seed, and for
// an endpoint model its thinking and streaming details; a model that
// cannot call tools leaves routing.
func (m *InteractiveMode) applyMeasure(s *setupModel, result *modelMeasure) {
	setupFilesMu.Lock()
	defer setupFilesMu.Unlock()
	dir := m.opts.AgentDir
	write := func(name string, merge func(string) (string, error)) {
		path := filepath.Join(dir, name)
		src, err := readConfigFile(path)
		if err != nil {
			return
		}
		if out, err := merge(src); err == nil {
			_, _ = writeConfigFile(path, out, time.Now(), false)
		}
	}
	if stat, ok := speedStat(result); ok {
		// A new measurement replaces an older setup measurement, never a
		// speed routing learned from real work.
		write(router.StatsFileName, func(src string) (string, error) {
			return mergeSpeedJSON(src, map[string]router.SpeedStat{s.spec(): stat})
		})
	}
	if s.Endpoint != nil && result.ok() {
		write("models.json", func(src string) (string, error) { return patchMeasuredModel(src, s, result) })
	}
	if result.Tools != nil && !*result.Tools {
		s.Existing, s.Edited, s.NoRouting = true, true, true
		write(router.ConfigFileName, func(src string) (string, error) {
			if strings.TrimSpace(src) == "" {
				return src, nil
			}
			return mergeRouterJSON(src, routerPlan{Existing: []*setupModel{s}})
		})
	}
	if m.opts.ModelRegistry != nil {
		m.opts.ModelRegistry.Refresh()
	}
}

// patchMeasuredModel records an endpoint model's measured thinking levels
// and quirks in its models.json definition.
func patchMeasuredModel(src string, s *setupModel, result *modelMeasure) (string, error) {
	index, err := definitionIndex(src, s.Provider, s.Model)
	if err != nil || index < 0 {
		return src, err
	}
	entry := []string{"providers", s.Provider, "models", "#" + strconv.Itoa(index)}
	set := func(value string, key string) error {
		var err error
		src, err = jsoncSet(src, value, append(append([]string(nil), entry...), key)...)
		return err
	}
	if err := set(strconv.FormatBool(result.Effort || result.Reasoning), "reasoning"); err != nil {
		return "", err
	}
	if len(result.LevelMap) > 0 {
		data, err := compactJSON(result.LevelMap)
		if err != nil {
			return "", err
		}
		if err := set(data, "thinkingLevelMap"); err != nil {
			return "", err
		}
	}
	if compat := compatFor(result); compat != nil {
		data, err := compactJSON(compat)
		if err != nil {
			return "", err
		}
		if err := set(data, "compat"); err != nil {
			return "", err
		}
	}
	return src, nil
}

// measureWords says what a measurement found, in plain words.
func measureWords(m *modelMeasure) string {
	if !m.ok() {
		return "Could not reach it: " + m.Err
	}
	var parts []string
	if m.Tools != nil {
		parts = append(parts, map[bool]string{true: "can use tools", false: "can't use tools"}[*m.Tools])
	}
	if m.Streaming != nil && !*m.Streaming {
		parts = append(parts, "sends each reply all at once")
	}
	if levels := thinkingSummary(m); levels != "" && levels != "yes" {
		parts = append(parts, "thinking levels "+strings.ReplaceAll(levels, ",", ", "))
	}
	speed := "starts answering in " + formatSeconds(m.TTFT.Seconds())
	if m.TPS > 0 {
		speed += fmt.Sprintf(", writes %.0f tokens a second", m.TPS)
	}
	parts = append(parts, speed)
	if m.PerK > 0 {
		parts = append(parts, "reads a long prompt at "+formatSeconds(m.PerK)+" per 1K tokens")
	}
	return strings.Join(parts, "; ")
}

// thinkingSummary lists the accepted effort levels, "yes" when the model
// thinks but its levels couldn't be probed, or "".
func thinkingSummary(m *modelMeasure) string {
	if m == nil || !m.Effort {
		if m != nil && m.Reasoning {
			return "yes"
		}
		return ""
	}
	return strings.Join(m.Accepted, ",")
}

// requestCost estimates what one tiny measuring request costs a
// pay-per-token model, from the catalog's prices, or 0 when unknown.
func requestCost(spec string) float64 {
	cat, ok := ai.LookupModelExact(spec)
	if !ok {
		return 0
	}
	const inTokens, outTokens = 30, 20
	return (inTokens*cat.InputCostPerMTokens + outTokens*cat.OutputCostPerMTokens) / 1e6
}

// reloadRouterQuietly picks up new measurements when nothing is running.
func (m *InteractiveMode) reloadRouterQuietly() {
	if m.setup != nil || m.turnActive.Load() || m.agent != nil && m.agent.IsStreaming() {
		return // setup reloads when it closes; a turn keeps its router
	}
	if reloader, ok := m.opts.SessionHandle.(interface{ ReloadRouter() error }); ok {
		_ = reloader.ReloadRouter()
	}
	m.tuiInst.RequestRender()
}
