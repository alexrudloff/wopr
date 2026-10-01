# Models

A model in WOPR is identified by a **provider-qualified spec**: `provider/modelID`. Every API that takes a model - `--model`, `/model`, F2 cycling, settings entries - should round-trip through provider-qualified specs. Bare model IDs are accepted for backward compatibility but are interpreted as `openai/<id>`, which has historically caused silent routing drift.

## Selecting a model

| Mechanism | Effect |
|---|---|
| `wopr --model openai/gpt-5.5` | Override for this process. |
| `/model` slash command | Open the selector: the models you set up in `/setup`, grouped by connection (routing modes first while model routing is on). `/model <provider/id>` picks any available model without adding it to that list. |
| `F2` / `Shift+F2`, Tab on an empty prompt | Cycle the models you set up in `/setup`. |

Autocomplete for `/model <name>` offers models from providers with configured authentication. It uses `GEMINI_API_KEY` for Google Gemini, not `GOOGLE_API_KEY`. Providers without configured authentication are omitted.

## Model specs

Model specs keep both `provider` and `id`, for example:

- `github-copilot/gpt-5.5`
- `openai/gpt-5.5-mini`
- `openrouter/openai/gpt-5.5` - trailing slash inside `id` is preserved.

If a custom helper passes only `model.ID` to `BuildModel()`, WOPR falls back to `openai/<id>` - never do this when switching models.

## Thinking levels

WOPR surfaces reasoning effort through these named levels:

```text
off → minimal → low → medium → high → xhigh → max
```

A model exposes only the levels it supports. Cycling follows that supported set.

- `Ctrl+T` cycles thinking on models that advertise reasoning support.
- The current label is rendered in the status line.
- Models that do not support reasoning silently ignore changes; the host does not emit an error.

A model is reasoning-capable when its generated entry sets `Reasoning: true`; `ThinkingLevelMap` controls how each level is wired into the request. See `ai/models_catalog.go` in source for the table.

## Model metadata

Every model entry the host knows about carries:

- `Provider` - provider key (see `providers.md`).
- `ID` - model identifier as the provider names it. May contain slashes.
- `DisplayName` - UI label.
- `Reasoning` - bool; whether the model supports `thinking_level`.
- `ThinkingLevelMap` - provider-specific wiring per level.
- `MaxTokens` / `ContextWindow` - for context usage math.
- `Input` - `text`, plus `image` for a model that accepts images. Every provider sends images only to models that list `image`; others get a one-line placeholder.
- `Cost` (if known) - input/output rates.

## Adding a model

New built-in models are added to `ai/models_catalog.go` in source. To add an OpenAI-compatible endpoint with static models, use `~/.wopr/agent/models.json`. See [Custom providers](custom-provider.md) and [Providers](providers.md).

## Common errors

| Symptom | Cause | Fix |
|---|---|---|
| `model gpt-5.5 routed to openai but I selected copilot` | Bare ID passed to `BuildModel()` | Always pass `provider/id`. |
| `GPT-5.5 does not support thinking` after a binary swap | Stale `wopr` on `PATH` | Reinstall or rebuild WOPR, run `hash -r`, and restart the TUI. |
| Thinking cycle has no effect | Active model has `Reasoning: false` | Switch to a reasoning model. |
| Model selector empty after `wopr login` | Credentials wrote to wrong root | Check `WOPR_HOME` vs `~/.wopr`. |
