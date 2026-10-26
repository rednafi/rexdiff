# Derived from charmbracelet/crush

Parts of `internal/diffview` derive from charmbracelet/crush.

- Source: https://github.com/charmbracelet/crush @ 17fccc0c6d4289f159384d3aaa7315ef1f71015b
  (`internal/ui/diffview`, `internal/ansiext`, `internal/ui/xchroma`).
- License: FSL-1.1-MIT. Copyright 2025-2026 Charmbracelet, Inc. See `LICENSE-crush.md`.
- Derived files: `view.go`, `render.go`, `highlight.go`, `style.go`, `split.go`, and
  `text.go`. Each one has a notice in its header comment. `fold.go` is not derived.

The code is now rexdiff's. It changes as rexdiff needs, and this file does not record
each change.
