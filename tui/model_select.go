package tui

// ModelSelectorItem is one model offered by the model picker and /model
// autocomplete.
type ModelSelectorItem struct {
	Provider string
	ID       string // bare model id (e.g. "gpt-4o")
	// Name is the raw model display name, empty when
	// the source has none. Search text omits an empty name; the selected-model
	// footer falls back to ID.
	Name string
}

// FQ returns the "<provider>/<id>" form used for switch dispatch.
func (m ModelSelectorItem) FQ() string { return m.Provider + "/" + m.ID }
