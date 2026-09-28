package codingagent

import "context"

// RefreshModelCatalogs reloads models.json for the interactive model selectors.
func RefreshModelCatalogs(ctx context.Context, registry *ModelRegistry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if registry != nil && registry.refreshContext(ctx) {
		return ctx.Err()
	}
	return nil
}
