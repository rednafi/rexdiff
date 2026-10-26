# Architecture

rexdiff is a Bubble Tea program in a Rex pane. It reads the repository with git, finds
the agents with rex, and pastes the review into the agent with rex. It talks to both
through their command-line tools.

![The packages of rexdiff](../.asset/arch.svg)

The diagram comes from `.asset/arch.json`. To edit it, open `.asset/arch.excalidraw` on
excalidraw.com.

## Packages

- `tui` has the screen and all the state. It only uses the packages below.
- `diff` draws a file diff in the terminal and keeps the comments.
- `vcs` runs git, and gh, glab, or az for the pull request.
- `rex` runs rex.
- `update` installs a new release and restarts rexdiff.
- `internal/` has small helpers for these packages.
- `cmd/rexdiff` reads the flags, runs `rexdiff install`, and starts `tui`.

## Rules of the design

- The libraries do not import each other, and `tui` uses only their public API.
- Only `cmd/rexdiff` reads the environment of the process. It gives the environment to
  the libraries as a value.
- Bubble Tea runs the model on one goroutine. Slow work runs in a command with a time
  limit, and sends back a message.
- Each scope compares two git trees. rexdiff makes the tree of your files with a private
  index, so the real index does not change.
- Lua does only the Rex part: the key and the action that opens the pane. Your settings
  are in `rexdiff.lua`. rexdiff writes the action to `rexdiff_toggle.lua`.
- When the right behaviour is not clear, rexdiff does what herdr-reviewr does.
