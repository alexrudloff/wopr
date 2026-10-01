// Package ai provides provider abstractions, model definitions, and
// auth storage for wopr.
//
// wopr keeps no hand-written model list: modeldb.go composes each
// provider's own model list, models.dev's metadata (an embedded snapshot,
// modelsdev.json, until a fresher copy arrives), and the per-provider and
// per-family rules in modeldb_rules.go.
package ai
