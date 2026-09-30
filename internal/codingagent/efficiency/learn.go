package efficiency

import (
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// Learning: every model starts from the global numbers (the SoL-Pi
// constants and the harness additions) and moves in small, bounded steps on
// what it does with them in real sessions. A knob moves only after enough
// events since it last moved, then its counts start over. Values are per
// provider/model and persist in LearnedFileName; deleting the file resets
// every model to the globals.

// LearnedFileName holds the learned values under the agent directory.
const LearnedFileName = "efficiency-learned.json"

const (
	learnedVersion = 1
	// learnMinSamples is how many events a knob needs before it moves.
	learnMinSamples = 8
	// learnMinCompactions is the same for the window share, which sees
	// fewer events.
	learnMinCompactions = 5
	// learnHorizon is how many provider requests a recall, readback, or
	// re-read may follow the event it answers.
	learnHorizon = 6
	// learnSaveEvery bounds how many requests pass between saves.
	learnSaveEvery = 10

	packThresholdMin, packThresholdMax = 4 * 1024, 32 * 1024
	packExcerptMin, packExcerptMax     = 512, 4 * 1024
	halfLifeKeepMin, halfLifeKeepMax   = 2, 12
	reducerMinBytesMin                 = 2 * 1024
	reducerMinBytesMax                 = 64 * 1024
	windowShareMin, windowShareMax     = 0.5, 1.0
	windowShareStep                    = 0.05
	// reducerRejectRate marks a reducer model unreliable; it still gets
	// every reducerProbeEvery-th call so it can recover.
	reducerRejectRate = 0.5
	reducerProbeEvery = 10
	// stallBandRequests and stallBandStalls are the least a context band
	// needs before stalls there lower a model's window share.
	stallBandRequests = 20
	stallBandStalls   = 3
	stallDecay        = 500
)

// ModelLearning is one model's learned values and the counts since each
// last moved. A zero value means the global default.
type ModelLearning struct {
	PackThreshold     int     `json:"packThreshold,omitempty"`
	PackExcerpt       int     `json:"packExcerpt,omitempty"`
	HalfLifeKeepDelta int     `json:"halfLifeKeepDelta,omitempty"`
	ReducerMinBytes   int     `json:"reducerMinBytes,omitempty"`
	WindowShare       float64 `json:"windowShare,omitempty"`
	// ReducerUnreliable marks a model whose receipts fail verification.
	ReducerUnreliable bool `json:"reducerUnreliable,omitempty"`

	Placed          int     `json:"placed,omitempty"`
	Recalled        int     `json:"recalled,omitempty"`
	Cut             int     `json:"cut,omitempty"`
	CutRecalled     int     `json:"cutRecalled,omitempty"`
	Receipts        int     `json:"receipts,omitempty"`
	Readbacks       int     `json:"readbacks,omitempty"`
	ReducerCalls    int     `json:"reducerCalls,omitempty"`
	ReducerRejected int     `json:"reducerRejected,omitempty"`
	ReducerSkips    int     `json:"reducerSkips,omitempty"`
	Compactions     int     `json:"compactions,omitempty"`
	Rereads         int     `json:"rereads,omitempty"`
	BandRequests    [10]int `json:"bandRequests"`
	BandStalls      [10]int `json:"bandStalls"`
	// Edits by kind, and how many failed: anchored (hashline) edits against
	// edits by exact text, which decide whether the model keeps anchors.
	AnchoredEdits int `json:"anchoredEdits,omitempty"`
	AnchoredFails int `json:"anchoredFails,omitempty"`
	TextEdits     int `json:"textEdits,omitempty"`
	TextFails     int `json:"textFails,omitempty"`
}

func clampInt(v, lo, hi int) int { return max(lo, min(hi, v)) }

// sanitize clamps loaded values, so a hand-edited or damaged file can never
// push a knob out of its range.
func (m *ModelLearning) sanitize() {
	if m.PackThreshold != 0 {
		m.PackThreshold = clampInt(m.PackThreshold, packThresholdMin, packThresholdMax)
	}
	if m.PackExcerpt != 0 {
		m.PackExcerpt = clampInt(m.PackExcerpt, packExcerptMin, packExcerptMax)
	}
	m.HalfLifeKeepDelta = clampInt(m.HalfLifeKeepDelta, -halfLifeKeepMax, halfLifeKeepMax)
	if m.ReducerMinBytes != 0 {
		m.ReducerMinBytes = clampInt(m.ReducerMinBytes, reducerMinBytesMin, reducerMinBytesMax)
	}
	switch {
	case math.IsNaN(m.WindowShare):
		m.WindowShare = 0
	case m.WindowShare != 0:
		m.WindowShare = math.Max(windowShareMin, math.Min(windowShareMax, m.WindowShare))
	}
	for i := range m.BandRequests {
		m.BandRequests[i], m.BandStalls[i] = max(0, m.BandRequests[i]), max(0, m.BandStalls[i])
	}
}

func (m *ModelLearning) packThreshold() int {
	return clampInt(cmp.Or(m.PackThreshold, ThresholdBytes), packThresholdMin, packThresholdMax)
}

func (m *ModelLearning) packExcerpt() int {
	return clampInt(cmp.Or(m.PackExcerpt, PlaceholderExcerptBytes), packExcerptMin, packExcerptMax)
}

func (m *ModelLearning) reducerMinBytes() int {
	return clampInt(cmp.Or(m.ReducerMinBytes, ReducerMinBytes), reducerMinBytesMin, reducerMinBytesMax)
}

func (m *ModelLearning) windowShare() float64 {
	if m.WindowShare == 0 {
		return windowShareMax
	}
	return math.Max(windowShareMin, math.Min(windowShareMax, m.WindowShare))
}

// scale multiplies v by f and clamps it.
func scale(v int, f float64, lo, hi int) int { return clampInt(int(float64(v)*f), lo, hi) }

// PackParams are ObservationPack's numbers for one model.
type PackParams struct {
	Threshold int
	Excerpt   int
}

// DefaultPackParams are the global numbers.
func DefaultPackParams() PackParams {
	return PackParams{Threshold: ThresholdBytes, Excerpt: PlaceholderExcerptBytes}
}

type recallKind int

const (
	recallPack recallKind = iota
	recallHalfLife
)

type pendingRecall struct {
	spec string
	kind recallKind
	at   int
}

type pendingReadback struct {
	spec string
	path string
	at   int
}

type pendingReread struct {
	spec  string
	files map[string]bool
	at    int
	hit   bool
}

// Learner observes one session and updates the shared learned values.
type Learner struct {
	mu      sync.Mutex
	dir     string
	on      bool
	models  map[string]*ModelLearning
	touched map[string]bool
	request int
	saved   int
	seen    map[string]bool
	recalls map[string]pendingRecall
	reads   []pendingReadback
	rereads []pendingReread
	// readFiles are the files read since the last compaction.
	readFiles map[string]bool
}

type learnedFile struct {
	Version int                       `json:"version"`
	Models  map[string]*ModelLearning `json:"models"`
}

// readLearned reads the file; a missing, damaged, or other-version file
// reads as empty.
func readLearned(dir string) map[string]*ModelLearning {
	data, err := os.ReadFile(filepath.Join(dir, LearnedFileName))
	if err != nil {
		return map[string]*ModelLearning{}
	}
	var f learnedFile
	if json.Unmarshal(data, &f) != nil || f.Version != learnedVersion || f.Models == nil {
		return map[string]*ModelLearning{}
	}
	for spec, m := range f.Models {
		if m == nil {
			delete(f.Models, spec)
			continue
		}
		m.sanitize()
	}
	return f.Models
}

// LoadLearner loads the learned values under dir. With on false it neither
// learns nor applies what was learned.
func LoadLearner(dir string, on bool) *Learner {
	l := &Learner{dir: dir, on: on, models: map[string]*ModelLearning{}, touched: map[string]bool{}, seen: map[string]bool{}, recalls: map[string]pendingRecall{}, readFiles: map[string]bool{}}
	if on && dir != "" {
		l.models = readLearned(dir)
	}
	return l
}

// On reports whether the learner is learning.
func (l *Learner) On() bool { return l != nil && l.on }

// model returns spec's entry, creating it; the caller holds mu.
func (l *Learner) model(spec string) *ModelLearning {
	m := l.models[spec]
	if m == nil {
		m = &ModelLearning{}
		l.models[spec] = m
	}
	return m
}

// peek returns spec's entry or an empty one; the caller holds mu.
func (l *Learner) peek(spec string) *ModelLearning {
	if m := l.models[spec]; m != nil {
		return m
	}
	return &ModelLearning{}
}

// PackParams returns ObservationPack's numbers for spec.
func (l *Learner) PackParams(spec string) PackParams {
	if !l.On() || spec == "" {
		return DefaultPackParams()
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	m := l.peek(spec)
	return PackParams{Threshold: m.packThreshold(), Excerpt: m.packExcerpt()}
}

// HalfLifeKeep returns how many newest tool results stay whole for spec.
func (l *Learner) HalfLifeKeep(spec string, window int) int {
	keep := HalfLifeKeep(window)
	if keep == 0 || !l.On() || spec == "" {
		return keep
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return clampInt(keep+l.peek(spec).HalfLifeKeepDelta, halfLifeKeepMin, halfLifeKeepMax)
}

// ReducerMinBytes returns the smallest log the reducer takes for spec.
func (l *Learner) ReducerMinBytes(spec string) int {
	if !l.On() || spec == "" {
		return ReducerMinBytes
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.peek(spec).reducerMinBytes()
}

// EffectiveWindow returns the window spec is given: its learned share of
// window, never more than window.
func (l *Learner) EffectiveWindow(spec string, window int) int {
	if !l.On() || spec == "" || window <= 0 {
		return window
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return min(window, int(float64(window)*l.peek(spec).windowShare()))
}

// ReducerUsable reports whether spec should be asked for a receipt: always,
// unless its receipts keep failing verification, and then every
// reducerProbeEvery-th time so it can recover.
func (l *Learner) ReducerUsable(spec string) bool {
	if !l.On() || spec == "" {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	m := l.peek(spec)
	if !m.ReducerUnreliable {
		return true
	}
	m = l.model(spec)
	m.ReducerSkips++
	l.touched[spec] = true
	return m.ReducerSkips%reducerProbeEvery == 0
}

// Tick records one provider request by spec at contextTokens of window,
// settles the events it no longer waits for, and saves now and then.
func (l *Learner) Tick(spec string, contextTokens, window int) {
	if !l.On() {
		return
	}
	l.mu.Lock()
	l.request++
	if spec != "" && window > 0 {
		m := l.model(spec)
		m.BandRequests[band(contextTokens, window)]++
		l.touched[spec] = true
	}
	l.settle(false)
	save := l.request-l.saved >= learnSaveEvery
	l.mu.Unlock()
	if save {
		l.Save()
	}
}

func band(tokens, window int) int { return clampInt(tokens*10/max(window, 1), 0, 9) }

// settle counts the events whose horizon passed (all of them with final)
// and moves the knobs that have enough samples; the caller holds mu.
func (l *Learner) settle(final bool) {
	expired := func(at int) bool { return final || l.request-at > learnHorizon }
	for id, p := range l.recalls {
		if !expired(p.at) {
			continue
		}
		m := l.model(p.spec)
		if p.kind == recallPack {
			m.Placed++
		} else {
			m.Cut++
		}
		delete(l.recalls, id)
	}
	l.reads = slices.DeleteFunc(l.reads, func(p pendingReadback) bool {
		if expired(p.at) {
			l.model(p.spec).Receipts++
			return true
		}
		return false
	})
	l.rereads = slices.DeleteFunc(l.rereads, func(p pendingReread) bool {
		if !p.hit && !expired(p.at) {
			return false
		}
		m := l.model(p.spec)
		m.Compactions++
		if p.hit {
			m.Rereads++
		}
		return true
	})
	for spec := range l.touched {
		l.model(spec).adjust()
	}
}

// adjust moves the knobs that have enough samples and restarts their
// counts.
func (m *ModelLearning) adjust() {
	if m.Placed >= learnMinSamples {
		switch rate := float64(m.Recalled) / float64(m.Placed); {
		case rate > 0.4:
			m.PackThreshold = scale(m.packThreshold(), 1.25, packThresholdMin, packThresholdMax)
			m.PackExcerpt = scale(m.packExcerpt(), 1.25, packExcerptMin, packExcerptMax)
		case rate < 0.1:
			m.PackThreshold = scale(m.packThreshold(), 0.8, packThresholdMin, packThresholdMax)
			m.PackExcerpt = scale(m.packExcerpt(), 0.8, packExcerptMin, packExcerptMax)
		}
		m.Placed, m.Recalled = 0, 0
	}
	if m.Cut >= learnMinSamples {
		switch rate := float64(m.CutRecalled) / float64(m.Cut); {
		case rate > 0.3:
			m.HalfLifeKeepDelta = min(m.HalfLifeKeepDelta+1, halfLifeKeepMax)
		case rate < 0.05:
			m.HalfLifeKeepDelta = max(m.HalfLifeKeepDelta-1, -halfLifeKeepMax)
		}
		m.Cut, m.CutRecalled = 0, 0
	}
	if m.Receipts >= learnMinSamples {
		switch rate := float64(m.Readbacks) / float64(m.Receipts); {
		case rate > 0.3:
			m.ReducerMinBytes = scale(m.reducerMinBytes(), 1.5, reducerMinBytesMin, reducerMinBytesMax)
		case rate < 0.05:
			m.ReducerMinBytes = scale(m.reducerMinBytes(), 0.75, reducerMinBytesMin, reducerMinBytesMax)
		}
		m.Receipts, m.Readbacks = 0, 0
	}
	if m.ReducerCalls >= learnMinSamples {
		m.ReducerUnreliable = float64(m.ReducerRejected)/float64(m.ReducerCalls) > reducerRejectRate
		m.ReducerCalls, m.ReducerRejected = 0, 0
	}
	if m.Compactions >= learnMinCompactions {
		share := m.windowShare()
		switch rate := float64(m.Rereads) / float64(m.Compactions); {
		case rate > 0.5:
			share += windowShareStep
		case rate < 0.2:
			share -= windowShareStep
		}
		m.WindowShare = math.Max(windowShareMin, math.Min(windowShareMax, share))
		m.Compactions, m.Rereads = 0, 0
	}
	m.adjustForStalls()
}

// adjustForStalls lowers the window share to the lowest band from which
// stalls are at least twice as frequent as below it.
func (m *ModelLearning) adjustForStalls() {
	total := 0
	for i := range m.BandRequests {
		total += m.BandRequests[i]
	}
	for b := 5; b < 10; b++ {
		upReq, upSt, lowReq, lowSt := 0, 0, 0, 0
		for i := range m.BandRequests {
			if i >= b {
				upReq, upSt = upReq+m.BandRequests[i], upSt+m.BandStalls[i]
			} else {
				lowReq, lowSt = lowReq+m.BandRequests[i], lowSt+m.BandStalls[i]
			}
		}
		if upReq < stallBandRequests || upSt < stallBandStalls {
			continue
		}
		lowRate := 0.0
		if lowReq > 0 {
			lowRate = float64(lowSt) / float64(lowReq)
		}
		if float64(upSt)/float64(upReq) >= 2*lowRate {
			m.WindowShare = math.Max(windowShareMin, math.Min(m.windowShare(), float64(b)/10))
			m.BandRequests, m.BandStalls = [10]int{}, [10]int{}
			return
		}
	}
	if total > stallDecay {
		for i := range m.BandRequests {
			m.BandRequests[i], m.BandStalls[i] = m.BandRequests[i]/2, m.BandStalls[i]/2
		}
	}
}

// Placed records that ObservationPack replaced observation id with its
// placeholder for spec.
func (l *Learner) Placed(spec, id string) { l.pend(spec, id, recallPack) }

// Cut records that half-life cut a tool result, archived as id, for spec.
func (l *Learner) Cut(spec, id string) { l.pend(spec, id, recallHalfLife) }

func (l *Learner) pend(spec, id string, kind recallKind) {
	if !l.On() || spec == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.seen[id] {
		return
	}
	l.seen[id] = true
	l.recalls[id] = pendingRecall{spec: spec, kind: kind, at: l.request}
	l.touched[spec] = true
}

// Recalled records an obs_recall of id.
func (l *Learner) Recalled(id string) {
	if !l.On() {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	p, ok := l.recalls[id]
	if !ok {
		return
	}
	delete(l.recalls, id)
	m := l.model(p.spec)
	if p.kind == recallPack {
		m.Placed++
		m.Recalled++
	} else {
		m.Cut++
		m.CutRecalled++
	}
}

// ReceiptApplied records that spec was given a reducer receipt whose raw
// log is archived at path.
func (l *Learner) ReceiptApplied(spec, path string) {
	if !l.On() || spec == "" || path == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.reads = append(l.reads, pendingReadback{spec: spec, path: path, at: l.request})
	l.touched[spec] = true
}

// ReducerVerdict records whether spec's receipt passed verification.
func (l *Learner) ReducerVerdict(spec string, ok bool) {
	if !l.On() || spec == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	m := l.model(spec)
	m.ReducerCalls++
	if !ok {
		m.ReducerRejected++
	}
	l.touched[spec] = true
}

// ToolCall records a tool call: a read of a receipt's raw log, a re-read of
// a file the last compaction summarized, or a file read.
func (l *Learner) ToolCall(name, args string) {
	if !l.On() {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.reads = slices.DeleteFunc(l.reads, func(p pendingReadback) bool {
		if strings.Contains(args, p.path) {
			m := l.model(p.spec)
			m.Receipts++
			m.Readbacks++
			return true
		}
		return false
	})
	if name != "read" {
		return
	}
	var in struct {
		Path string `json:"path"`
	}
	if json.Unmarshal([]byte(args), &in) != nil || in.Path == "" {
		return
	}
	for i := range l.rereads {
		if l.rereads[i].files[in.Path] {
			l.rereads[i].hit = true
		}
	}
	l.readFiles[in.Path] = true
}

// Compacted records a compaction while spec served the conversation; a
// re-read of the files read before it counts against compacting early.
func (l *Learner) Compacted(spec string) {
	if !l.On() || spec == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.readFiles) > 0 {
		l.rereads = append(l.rereads, pendingReread{spec: spec, files: l.readFiles, at: l.request})
	}
	l.readFiles = map[string]bool{}
	l.touched[spec] = true
}

// Stalled records that spec stalled at contextTokens of window.
func (l *Learner) Stalled(spec string, contextTokens, window int) {
	if !l.On() || spec == "" || window <= 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.model(spec).BandStalls[band(contextTokens, window)]++
	l.touched[spec] = true
}

// Save writes the models this session touched into the file, keeping other
// sessions' entries for the rest.
func (l *Learner) Save() {
	if !l.On() || l.dir == "" {
		return
	}
	l.mu.Lock()
	l.saved = l.request
	touched := map[string]ModelLearning{}
	for spec := range l.touched {
		touched[spec] = *l.model(spec)
	}
	l.mu.Unlock()
	if len(touched) == 0 {
		return
	}
	models := readLearned(l.dir)
	for spec, m := range touched {
		models[spec] = &m
	}
	data, err := json.MarshalIndent(learnedFile{Version: learnedVersion, Models: models}, "", "  ")
	if err != nil {
		return
	}
	path := filepath.Join(l.dir, LearnedFileName)
	tmp := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	if os.WriteFile(tmp, append(data, '\n'), 0o600) != nil {
		return
	}
	if os.Rename(tmp, path) != nil {
		_ = os.Remove(tmp)
	}
}

// Close settles every open event and saves.
func (l *Learner) Close() {
	if !l.On() {
		return
	}
	l.mu.Lock()
	l.settle(true)
	l.mu.Unlock()
	l.Save()
}

// Anchors turn off for a model once its anchored edits fail at least
// anchorFailRate of the time over anchorMinEdits or more, and at least
// anchorFailRatio times as often as its exact-text edits.
const (
	anchorMinEdits  = 8
	anchorFailRate  = 0.2
	anchorFailRatio = 3.0
)

// anchorsFailing is the decision rule for one model's edit counts.
func (m *ModelLearning) anchorsFailing() bool {
	if m.AnchoredEdits < anchorMinEdits {
		return false
	}
	rate := float64(m.AnchoredFails) / float64(m.AnchoredEdits)
	textRate := float64(m.TextFails) / float64(max(m.TextEdits, 1))
	return rate >= anchorFailRate && rate >= anchorFailRatio*textRate
}

// EditResult records one edit by spec: anchored or by exact text, and
// whether it failed.
func (l *Learner) EditResult(spec string, anchored, failed bool) {
	if !l.On() || spec == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	m := l.model(spec)
	if anchored {
		m.AnchoredEdits++
		if failed {
			m.AnchoredFails++
		}
	} else {
		m.TextEdits++
		if failed {
			m.TextFails++
		}
	}
	l.touched[spec] = true
}

// AnchorsOff reports whether spec's anchored edits fail clearly more often
// than its exact-text edits, so hashline's "auto" should leave them off.
func (l *Learner) AnchorsOff(spec string) bool {
	if !l.On() || spec == "" {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.peek(spec).anchorsFailing()
}

// Summary lists, per model, the learned values that differ from the
// globals.
func (l *Learner) Summary() []string {
	if !l.On() {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, spec := range slices.Sorted(maps.Keys(l.models)) {
		m := l.models[spec]
		var parts []string
		if v := m.packThreshold(); v != ThresholdBytes {
			parts = append(parts, fmt.Sprintf("pack over %d KB", v/1024))
		}
		if v := m.packExcerpt(); v != PlaceholderExcerptBytes {
			parts = append(parts, fmt.Sprintf("excerpt %d B", v))
		}
		if m.HalfLifeKeepDelta != 0 {
			parts = append(parts, fmt.Sprintf("half-life keep %+d", m.HalfLifeKeepDelta))
		}
		if v := m.reducerMinBytes(); v != ReducerMinBytes {
			parts = append(parts, fmt.Sprintf("reduce over %d KB", v/1024))
		}
		if v := m.windowShare(); v != windowShareMax {
			parts = append(parts, fmt.Sprintf("window %.0f%%", v*100))
		}
		if m.ReducerUnreliable {
			parts = append(parts, "receipts unreliable")
		}
		if m.anchorsFailing() {
			parts = append(parts, fmt.Sprintf("anchors off (%d of %d anchored edits failed)", m.AnchoredFails, m.AnchoredEdits))
		}
		if len(parts) > 0 {
			out = append(out, spec+": "+strings.Join(parts, ", "))
		}
	}
	return out
}
