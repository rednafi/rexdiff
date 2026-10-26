<img src="assets/logo.png" alt="rexdiff" width="360" align="right">

A diff viewer for coding agents in [Rex]. Think [herdr-reviewr], but for Rex.


It opens next to the agent and shows the changes. You comment on lines and send
the comments to the agent.

https://github.com/user-attachments/assets/e44159eb-3b02-4ea8-88ca-f6314261c1aa

## Install

With Homebrew:

```sh
brew tap rednafi/rexdiff https://github.com/rednafi/rexdiff
brew trust rednafi/rexdiff
brew install --cask rexdiff
```

With Go:

```sh
go install github.com/rednafi/rexdiff/cmd/rexdiff@latest
```

Add rexdiff to Rex:

```sh
rexdiff install
```

This adds the "Toggle rexdiff" action and binds it to Shift+Cmd+R. To remove
them, run `rexdiff uninstall`.

## Use

- Open: press Shift+Cmd+R next to an agent, or run "Toggle rexdiff" from the
  command palette. Press it again to close rexdiff.
- Comment: move to a line and press `c`. For a range, press `v` or drag over
  the line numbers.
- Send: press `s`. rexdiff pastes the comments into the agent's input. It does
  not press Enter.
- Update: when rexdiff shows a new version at the bottom, press `U`. Your
  comments stay.

When the agent edits a file, the comments move with their lines. If the lines
are gone, rexdiff marks the comment as stale.

| Key | Action |
| --- | --- |
| `u` `b` `t` | uncommitted, branch, or last-turn changes |
| `]` `[` | next or previous change |
| `f` `F` | next or previous file |
| `c` | comment |
| `s` | send the comments to the agent |
| `y` | copy the comments |
| `\|` | side by side |
| `1` `2` `3` | changes, all files, or pull request |
| `?` | all keys |

## Configure

Edit the settings at the top of `~/.config/rex/rexdiff.lua`:

```lua
local settings = {
  key = "shift+cmd+r",   -- toggle key, or false for none
  placement = "split",   -- split, zoomed, tab, or overlay
  layout = "split",      -- unified or split
  editor = "code -g {file}:{line}",
  keybindings = { send = { "S" } },
}
```

Other settings:

- `default_scope`
- `markdown_view`
- `file_list`
- `file_list_width`
- `review_prefix`
- `url_opener`
- `focus_agent_after_send`
- `github_host`
- `gitlab_host`
- `azure_devops_host`

See [herdr-reviewr] for the values and the action names.

`e` opens the file at the cursor line in `editor`, else in `$VISUAL`, `$EDITOR`, or git's
`core.editor`. A terminal editor, such as `nvim`, `micro`, or `fresh`, opens in a new pane
beside rexdiff. A window editor, such as `code`, opens in its window.

After a change:

- Run `rex config reload`.
- Do not edit the line `-- rexdiff code below` or the code under it.

## Requirements

- Rex
- git
- `gh`, `glab`, or `az` for the pull request tab. You must be logged in.

<!-- references -->

[Rex]: https://www.superlogical.com/rex
[herdr-reviewr]: https://github.com/persiyanov/herdr-reviewr
