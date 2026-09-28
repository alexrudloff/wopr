package tui

// Model search text builders for /model argument autocomplete.

// ModelSearchItem is the provider, bare id, and optional display name a model
// search text is built from. An empty Name means no display name.
type ModelSearchItem struct {
	ID       string
	Provider string
	Name     string
}

// GetModelSearchText builds model search text: the bare id leads,
// followed by the provider, the provider/id pair, provider and id again, and
// the name when present.
func GetModelSearchText(item ModelSearchItem) string {
	id, provider := item.ID, item.Provider
	name := ""
	if item.Name != "" {
		name = " " + item.Name
	}
	return id + " " + provider + " " + provider + "/" + id + " " + provider + " " + id + name
}
