# User guide

rexdiff shows what a coding agent changed in your repository, in a Rex pane beside the
agent. You comment on lines and send the comments to the agent as one review.

## Install

```sh
brew tap rednafi/rexdiff https://github.com/rednafi/rexdiff
brew trust rednafi/rexdiff
brew install --cask rexdiff
rexdiff install
```

With Go, replace the brew lines with
`go install github.com/rednafi/rexdiff/cmd/rexdiff@latest`.

`rexdiff install` adds the "Toggle rexdiff" action to Rex and binds it to Shift+Cmd+R.
To open rexdiff, focus the agent's pane and press Shift+Cmd+R. Press it again to close
rexdiff.

`rexdiff uninstall` removes rexdiff from Rex's config.

## Read the changes

![The Changes tab](../.asset/changes.png)

The diff is on the left and the changed files are on the right. A red bar marks a
removed line, and a green bar marks an added line. A long run of unchanged lines folds
into one row. Press the right arrow key on that row to show the lines.

`j` and `k` move one line. `]` and `[` jump between changes, and `f` and `F` jump between
files. rexdiff reloads when a file changes.

The scope sets what the diff compares against:

- `u`: uncommitted changes
- `b`: all changes on the branch
- `t`: the agent's last turn
- `g`: commits you pick

Press `|` to see the old and new code side by side. This needs a wide pane, and `z` hides
the file list to make room.

![Side by side](../.asset/split.png)

## Comment

Put the cursor on a line, press `c`, type the comment, and press Enter. For several
lines, press `v` and move down first, or drag over the line numbers.

![Writing a comment](../.asset/comment.png)

The comment shows under its code, and the file list counts the comments in each file.

![Comments in the diff](../.asset/review.png)

`e` edits the comment under the cursor, `d` deletes it, and `l` lists all comments.

![The comment list](../.asset/list.png)

## Send

Press `s`. rexdiff pastes the review into the agent's input box and does not press
Enter.

![The review in the agent](../.asset/sent.png)

Each comment carries the file, the line numbers, and the code. When more than one agent
works in the repository, rexdiff asks which one gets the review.

Press `y` to copy the review instead, for example outside Rex. Sending and copying both
clear the comments.

## Other views

`2` shows all files in the repository. You can comment there too.

![All files](../.asset/files.png)

`/` searches file names, and Tab switches to searching code.

![Search](../.asset/search.png)

`3` shows the pull request for the branch, with its checks and recent comments. It
needs gh, glab, or az, and you must be logged in.

`e` on a line without a comment opens the file in your editor at that line.

`?` shows every key.

![All keys](../.asset/help.png)

## Settings

The settings are in `rexdiff.lua`, next to Rex's `init.lua`. That is
`~/.config/rex/` unless `REX_CONFIG` or `XDG_CONFIG_HOME` moves it.

```lua
local settings = {
  key = "shift+cmd+r",   -- toggle key, or false for none
  placement = "split",   -- split, zoomed, tab, or overlay
  layout = "unified",    -- or split
  editor = "code -g {file}:{line}",
  keybindings = { send = { "S" } },
}
```

Also available: `default_scope`, `markdown_view`, `file_list`, `file_list_width`,
`diff_signs`, `review_prefix`, `url_opener`, `focus_agent_after_send`, `github_host`,
`gitlab_host`, and `azure_devops_host`. They take the same values as in
[herdr-reviewr](https://github.com/persiyanov/herdr-reviewr).

After a change, run `rex config reload`. Do not edit `rexdiff_toggle.lua`, because rexdiff
writes it again.

Without `editor`, rexdiff uses `$VISUAL`, `$EDITOR`, or git's `core.editor`, read from
your login shell. With zsh, set them in `~/.zprofile`, not `~/.zshrc`.

## Keys

| Key | Action |
| --- | --- |
| `j` `k`, `↓` `↑` | move down or up |
| `ctrl+d` `ctrl+u`, `pgdn` `pgup` | half a page, or a page |
| `]` `[` | next or previous change |
| `f` `F` | next or previous file |
| `n` `N` | next or previous comment |
| `:` | go to a line (`0` first, `$` last) |
| `tab` | switch focus between the diff and the file list |
| `→` `←` | expand a fold or directory, collapse a directory, or scroll sideways |
| `u` `b` `t` | uncommitted, branch, or last-turn changes |
| `g` `G` | commits scope, or pick commits |
| `B` | pick the base branch |
| `1` `2` `3` | the Changes, Files, or PR tab |
| `v` | select lines |
| `c` | comment |
| `e` | edit the comment, or open the file in your editor |
| `d` | delete the comment |
| `l` | list comments |
| `s` `S` | send comments to the agent |
| `y` `Y` | copy comments instead of sending |
| `/` | search files (`tab` for code) |
| `ctrl+f` | find in the file |
| `\|` | side by side |
| `w` | wrap long lines |
| `m` | rendered markdown |
| `z` | hide or show the file list |
| `p` | move the file list |
| `<` `>` | resize the file list |
| `o` | open the pull request in the browser |
| `r` | refresh |
| `U` | update rexdiff |
| `?` | all keys |
| `q` `Q` | quit, or quit and drop unsent comments |

## Updates

When the bottom line shows a new version, press `U`. rexdiff restarts in the same pane
and keeps your comments.

## Details

### Reloads

rexdiff checks the files about once a second without running git, and runs git only
after a change. In a large repository it checks less often, to stay under about 5% of a
core. `r` reloads at once. If a reload fails, rexdiff keeps the files on screen, shows
"refresh failed", and retries with a growing delay, up to a minute. While the comment
box is open, the view does not change.

The pull request tab reloads each minute while it shows, and after a commit, a push, or a
fetch. Switching branches clears it until the new pull request loads.

### Last turn

A turn starts when an agent in this repository starts to work and ends when all of them
rest. `t` shows the changes since the start of the last turn that changed a file. Before
the first turn it shows "waiting for the first turn", or "no agent works here". If a file
changes just as a turn starts, rexdiff cannot tell what the turn changed, and says so.

### Comments

A comment stays on its line numbers and keeps the code it was made on. It shows only in
the view where you made it (changes or all files) and on the same commits. If its file
leaves the changes, or is deleted, the comment list marks it "stale". An edit with blank
text keeps the comment as it was.

When `q` asks before quitting, `Q` quits and drops the comments, `s` sends, `y` copies,
and any other key closes the question.

### File list and search

The file list is a tree. Directories start expanded in the changes and collapsed in all
files, where a collapsed directory with a change has a dot. Ignored directories are dim
and load their files when you expand them.

At a side, the list takes 32% of the width (15–60% with `<` `>`). Above or below, it takes
a quarter of the rows (15–50%).

The search shows up to 50 files and 200 lines of code. A code search stops 80 ms after its
first match and marks a partial count with `+`. With no query, it lists the files you opened from the
search first.

### Side by side

Side by side needs a diff pane at least 137 columns wide. A narrower pane shows one
column.

### Base and commits

rexdiff keeps the base pick and the last turn in `refs/worktree/rexdiff/`, so they
survive a restart. With `-base`, the base picker does not open. `rexdiff -h` lists the
flags. The commit picker lists
the branch's own commits (first parent only), or the last 50 on the base branch.

### Copy and editor

Copying uses the first of `pbcopy`, `wl-copy`, `xclip`, or `xsel` on your PATH. Drag over
code to copy lines, double-click for a word, triple-click for a line, and double-click a
file in the list to copy its path.

A terminal editor, such as `nvim`, opens in a new pane beside rexdiff. A window editor,
such as `code`, opens in its own window. An editor that rexdiff does not know opens in a pane.
To open it in a window, start it with `open`, for example `editor = "open -a Zed {file}"`.

`keybindings` takes the action names of herdr-reviewr, and also `focus` (switch pane)
and `update`. A key is one character, or `left`, `right`, `up`, `down`, `pageup`, or
`pagedown`, alone or after `ctrl+` or `alt+`. Binding one key to two actions is an error.

## Problems

- "not in Rex": rexdiff started outside Rex and cannot send. Press `y` to copy, or
  open rexdiff with Shift+Cmd+R.
- "no agent in this session": start the agent in the same Rex session.
- "no editor": set `editor` in the settings, or set `$EDITOR` in `~/.zprofile`. rexdiff
  reads the login shell, so `~/.zshrc` has no effect.
- Side by side shows one column: the diff pane is narrower than 137 columns. Press `z`
  or widen the pane.
- The PR tab shows an error: install gh, glab, or az, and log in.
