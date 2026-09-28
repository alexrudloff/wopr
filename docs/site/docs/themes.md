# Themes

WOPR uses themes to style its terminal interface. Its default theme is `wopr`, a neon palette on deep navy. It also bundles other themes (`opencode`, `tokyonight`, `catppuccin`, `gruvbox`, `nord`, `dracula`, and more).

## Select a theme

Press `ctrl+x t`, or choose **Switch theme** in the command palette (`ctrl+p`). Moving through the list previews each theme live; Enter keeps it and Escape restores the previous one. WOPR saves the choice to your settings.

You can also set the name directly in `~/.wopr/agent/settings.json`:

```json
{
  "theme": "tokyonight"
}
```

The built-in names `dark` and `light` select the default theme's dark and light variants.

## opencode themes

WOPR reads opencode theme files as they are, so a theme you use in opencode works in WOPR. It loads them from:

- `~/.config/opencode/themes/` (or `$XDG_CONFIG_HOME/opencode/themes/`);
- `.opencode/themes/` in a trusted project and its parent directories.

An opencode theme is a JSON file with a `defs` table of named colors and a `theme` table of tokens. A token's value is a hex color, a name from `defs` or another token, a 256-color index, `none` for the terminal default, or a `{"dark": ..., "light": ...}` pair chosen by the terminal's background. The file's name is the theme's name. A WOPR theme with the same name takes precedence.

## Theme sources

WOPR can load theme Resources from:

- `~/.wopr/agent/themes/`;
- trusted project Resources;
- explicit settings paths.

Project theme discovery requires project trust.

## Reload

Use `/reload` after you change a theme file or Resource configuration:

```text
/reload
```

Theme parse errors are reported instead of silently selecting another authored theme.

## Related documentation

- [Settings](settings.md)
- [Terminal UI](tui.md)
