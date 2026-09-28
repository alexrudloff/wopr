# Terminal setup

WOPR runs in any terminal. Some features need terminal support, and WOPR turns each
one on only when it detects that support. This page lists what WOPR detects, and
what to change when a key or an image does not work.

## Troubleshooting

| Symptom | Where to look |
|---|---|
| `Shift+Enter` submits instead of adding a line | The section for your terminal below. Inside tmux, see [tmux](#tmux). |
| `Alt+Enter` does not queue a follow-up | [WezTerm](#wezterm), [Alacritty](#alacritty) or [Windows Terminal](#windows-terminal) |
| Scrolling in fullscreen mode is slow | [iTerm2](#iterm2) |
| Links show no hover preview in fullscreen mode | [Ghostty](#ghostty) |
| An input method window appears in the wrong place | [IME candidate window](#ime-candidate-window) |
| A key works outside WOPR but not inside it | [Keys that do not respond](#keys-that-do-not-respond) |

Use `/hotkeys` to list the keys WOPR has bound. `Ctrl+J` always inserts a new line, so you can use it in any terminal where `Shift+Enter` does not arrive.

## What WOPR detects

WOPR reads environment variables to decide what your terminal accepts. It does not
ask the terminal, because a terminal that does not answer would delay startup.

| Terminal | Images | True color | Hyperlinks |
|---|---|---|---|
| Kitty | yes | yes | yes |
| Ghostty | yes | yes | yes |
| WezTerm | yes | yes | yes |
| iTerm2 | yes | yes | yes |
| Warp | yes | yes | yes |
| VS Code | no | yes | yes |
| Alacritty | no | yes | yes |
| Zed | no | yes | yes |
| Windows Terminal | no | yes | yes |
| JetBrains | no | yes | no |
| Apple Terminal | no | with `COLORTERM` | no |

Inside `tmux` or `screen`, WOPR turns images off. The multiplexer
sits between WOPR and the terminal, so what the outer terminal accepts is not
evidence that the pane accepts it. Hyperlinks stay off in `screen`. In `tmux`,
WOPR asks tmux whether the attached client forwards hyperlinks and uses them
only when it does.

WOPR reads `TERM_PROGRAM`, `TERM` and `COLORTERM` first. It also accepts
`KITTY_WINDOW_ID`, `GHOSTTY_RESOURCES_DIR`, `WEZTERM_PANE`, `ITERM_SESSION_ID`,
`WARP_SESSION_ID` and `WT_SESSION` as evidence, because some terminals set only
these. A terminal it does not recognize gets true color only with
`COLORTERM=truecolor` or `COLORTERM=24bit`, and no images or hyperlinks.

To correct detection, set `WOPR_HYPERLINKS`, `WOPR_IMAGE_PROTOCOL` or `WOPR_TRUE_COLOR`
(see [environment variables](environment-variables.md)), or the matching
`terminal.*` setting (see [settings](settings.md)). The setting wins.

## Keys that do not respond

A terminal sends a key to WOPR only if it does not use the key itself. When a
binding does nothing, the terminal usually consumed the key.

Check first whether the key works outside WOPR. If the terminal has a command
bound to it, unbind the command or bind the WOPR action to a different key. See
[keybindings](keybindings.md).

### tmux

`tmux` must send extended keys, or modified keys such as `shift+enter` and
`ctrl+enter` arrive as their unmodified form. WOPR warns at startup when it finds
the wrong setting.

Add both lines to `~/.tmux.conf` and restart tmux:

```tmux
set -g extended-keys on
set -g extended-keys-format csi-u
```

`extended-keys-format xterm` also sends modified keys, but in a format that
carries less information. WOPR warns about it and works better with `csi-u`.

WOPR does not warn when it cannot query tmux, because a sandbox that blocks the
query is not evidence of a wrong setting.

### Apple Terminal

WOPR enables enhanced key reporting when available. If Terminal.app still sends
plain Return for `shift+enter`, WOPR asks macOS whether Shift is held and
treats the key as `shift+enter`. This works only when WOPR runs on the same Mac
as Terminal.app. Over SSH, WOPR cannot see the local modifier state.

### Termux

Termux reports its window size differently from a desktop terminal. WOPR detects
Termux with `TERMUX_VERSION` and does not repaint the whole buffer when the
window changes, because the repaint costs more than it corrects there.

## Terminal-specific setup

WOPR asks the terminal for the Kitty keyboard protocol at startup and falls back to xterm `modifyOtherKeys`. Either one lets WOPR tell `Shift+Enter` and `Alt+Enter` apart from `Enter`. The entries below cover terminals that need a change to send those keys.

### Kitty

Kitty supports the Kitty keyboard protocol. It needs no setup.

### iTerm2

The regular layout needs no setup.

WOPR captures the mouse, so iTerm2 sends wheel events to WOPR instead of scrolling its own history. Fast trackpad gestures can then scroll only one line at a time. To change this, open **iTerm2 > Settings > Advanced**, find **Trackpad scrolls fast?** and set it to **No**. The setting applies to all of iTerm2.

### Apple Terminal

Terminal.app can send a plain `Enter` for `Shift+Enter`. Use `Ctrl+J` to insert a new line there.

### Ghostty

If `Alt+Backspace` does not delete a word, add this line to Ghostty's configuration file:

```text
keybind = alt+backspace=text:\x1b\x7f
```

The file is `~/Library/Application Support/com.mitchellh.ghostty/config` on macOS and `~/.config/ghostty/config` on Linux.

Remove a `keybind = shift+enter=text:\n` line if another tool added it. That line makes `Shift+Enter` send a raw line feed, which is the same byte as `Ctrl+J`. It appears to work in WOPR, but WOPR and tmux no longer receive a real `Shift+Enter`.

In fullscreen mode, WOPR captures the mouse, so Ghostty does not underline links or preview their URLs on hover. Links are still clickable. Hold the modifier that Ghostty uses to bypass mouse capture to get its own link handling.

### WezTerm

WezTerm reports `Shift+Enter` without setup. To use the Kitty keyboard protocol, set `enable_kitty_keyboard` in `~/.wezterm.lua`:

```lua
local wezterm = require 'wezterm'
local config = wezterm.config_builder()
config.enable_kitty_keyboard = true
return config
```

On macOS, WezTerm uses `Option+Enter` for its own fullscreen toggle. To send it to WOPR as `Alt+Enter`, add this entry to `config.keys`:

```lua
config.keys = {
  {
    key = 'Enter',
    mods = 'ALT',
    action = wezterm.action.SendString('\x1b[13;3u'),
  },
}
```

### Alacritty

Alacritty reports `Shift+Enter`. On macOS, `Option+Enter` can arrive as a plain `Enter`. Add this binding to `~/.config/alacritty/alacritty.toml` and restart Alacritty:

```toml
[[keyboard.bindings]]
key = "Enter"
mods = "Alt"
chars = "\u001b[13;3u"
```

### VS Code

If `Shift+Enter` submits in the integrated terminal, add this entry to VS Code's `keybindings.json`:

```json
{
  "key": "shift+enter",
  "command": "workbench.action.terminal.sendSequence",
  "args": { "text": "\u001b[13;2u" },
  "when": "terminalFocus"
}
```

The file is in `~/Library/Application Support/Code/User/` on macOS, `~/.config/Code/User/` on Linux and `%APPDATA%\Code\User\` on Windows.

### Zed

Add these bindings to Zed's `keymap.json`. They send `Shift+Enter` (new line), `Ctrl+-` (undo) and `Ctrl+Alt+]` (jump backward to a character) to WOPR:

```json
{
  "context": "Terminal",
  "bindings": {
    "shift-enter": ["terminal::SendText", "\u001b[13;2u"],
    "ctrl--": ["terminal::SendText", "\u001b[45;5u"],
    "ctrl-alt-]": ["terminal::SendText", "\u001b[93;7u"]
  }
}
```

### Windows Terminal

On Windows and in WSL, WOPR uses Windows defaults for some keys. For example, `Ctrl+Q` queues a follow-up, because Windows Terminal uses `Alt+Enter` for fullscreen. See [keybindings](keybindings.md).

To send `Shift+Enter`, open Windows Terminal's `settings.json` (**Settings > Open JSON file**) and add this object to the `actions` array:

```json
{
  "command": { "action": "sendInput", "input": "\u001b[13;2u" },
  "keys": "shift+enter"
}
```

Close every Windows Terminal window and start it again.

To queue follow-ups with `Alt+Enter` instead, make Windows Terminal send the key the same way with `\u001b[13;3u`, then bind `app.message.followUp` to `alt+enter` in `~/.wopr/agent/keybindings.json`.

### xfce4-terminal and Terminator

These terminals cannot report modified `Enter` keys. `Shift+Enter` and `Ctrl+Enter` bindings do not work there. Use `Ctrl+J` for a new line, or use a terminal with extended key support such as Kitty, Ghostty, WezTerm, iTerm2 or Windows Terminal.

### IntelliJ IDEA

The IntelliJ terminal cannot tell `Shift+Enter` from `Enter`. Use `Ctrl+J` for a new line, or run WOPR in another terminal.

### IME candidate window

WOPR draws its own cursor. Some input methods, for example CJK input in the IntelliJ terminal or in WezTerm under WSL, then place their candidate window in the wrong position. Show the terminal's cursor so the input method can follow it:

```bash
WOPR_HARDWARE_CURSOR=1 wopr
```

The `showHardwareCursor` setting does the same.

## Images

WOPR shows images when the terminal accepts the Kitty graphics protocol or the
iTerm2 protocol. In every other terminal WOPR writes the file path instead, so no
information is lost.

Images are off inside `tmux` and `screen`. To see images, run WOPR outside the
multiplexer, or set `WOPR_IMAGE_PROTOCOL` when you know the multiplexer forwards
them.

## Capability overrides

Set `WOPR_HYPERLINKS`, `WOPR_IMAGE_PROTOCOL` or `WOPR_TRUE_COLOR` to override the detection above. The matching `terminal.hyperlinks`, `terminal.images` and `terminal.trueColor` settings win over the variables.

For images, use the `terminal.showImages` setting or `/settings` to turn images off. For colors, set `COLORTERM=truecolor` when your terminal supports true color but WOPR does not detect it.

## Display problems

| Symptom | Cause | What to do |
|---|---|---|
| colors look wrong | the terminal reports no true color | set `COLORTERM=truecolor` if the terminal supports it |
| a stray `[?62;22c` appears | the terminal answered a capability query late | WOPR consumes this answer; report it with the terminal name |

WOPR clips a row wider than the terminal to the terminal width.

## Related

- [Keybindings](keybindings.md) lists the actions and their default keys.
- [Settings](settings.md) lists the files and settings WOPR reads.
