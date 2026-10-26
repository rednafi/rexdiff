# Vendored from charmbracelet/crush

- Source: https://github.com/charmbracelet/crush @ 17fccc0c6d4289f159384d3aaa7315ef1f71015b
- Paths: internal/ui/diffview, internal/ansiext, internal/ui/xchroma, all merged into
  `internal/diffview`, because they only serve the diff view
- License: FSL-1.1-MIT (see LICENSE-crush.md). Copyright Charmbracelet, Inc.
- Local changes:
  - package and import paths (module `github.com/rednafi/rexdiff`)
  - `escape.go` is crush's `ansiext/ansi.go`; `chroma.go` is crush's `xchroma/chroma.go`
  - `rows.go` (added): the row to source line mapping (with the continued rows of wrapped
    lines) and the column geometry, which `Render` returns with the text
  - `View` is crush's `DiffView`, and `Render` is its `String`
  - `terminal.go` (added): `TerminalStyle`, the colours from the terminal palette, and the
    `Wrap` option
  - `ansi.go` (added): a chroma formatter that uses the ANSI palette colours
  - `diffview.go`: the code is always highlighted with the formatter of
    `ansi.go` (crush's `ChromaStyle` and chroma style formatter are removed);
    `Lexer`, the lexer selection that `diff.RenderFile` shares; the
    syntax cache key is a struct, not an xxh3 hash; `New` sets no default style; the code
    lines of the two renderers go through one `codeLines`, which cuts or wraps a line, and
    the split renderer builds each side with one `splitSide` method; named constants
    for the symbols and the ellipsis; smaller render helpers
  - `split.go`: uses `slices` in place of `charmbracelet/x/exp/slice`; `takeInsert` finds
    the inserted line beside a deleted line
  - `chroma.go`: only the lexer cache `matchLexer` and `renderLines`, which
    the ANSI formatter uses; crush's `Formatter` and its token loop are removed
  - `style.go`: `DefaultDarkStyle` and `DefaultLightStyle` removed (and with them
    `charmtone`); the `diff` package always uses `TerminalStyle`
  - API cut to what `diff` uses: removed `ChromaStyle`, `Formatter`, `Unified`,
    `FileName` (and the file name header), `ContextLines`, `LineNumbers`,
    `Height`, `YOffset`, `InfiniteYScroll` (and the height cut and the scroll
    code); `MatchLexer`, `LineStyle` and the `Style`
    fields are unexported; `RowFile` removed
  - a coloured bar `▌` marks a changed line, in place of crush's `+` and `-`; `Signs` gives
    the signs back
  - `fold.go` (added): the diff has the whole file in one hunk, and `foldHunk` folds the
    long runs of equal lines, as in herdr-reviewr; `Show` shows a fold, and `Row.Fold` tells
    which lines a hunk header hides
  - the syntax cache key tells "no background" from black, so a black tint does not
    leak onto equal lines
