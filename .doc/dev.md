# Development

You need Go at the version in `go.mod`, git, and golangci-lint v2.15.0. Older
golangci-lint releases can fail on newer Go.

```sh
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0
```

## Run a change in Rex

```sh
./dev.sh
```

`dev.sh` builds rexdiff and opens the Rex session `rexreview-test`, with a pane labelled
"agent" and rexdiff beside it. Run it again after each change, and it replaces the old
pane. The log goes to `/tmp/rexdiff.log`.

It shows `/tmp/rexreview-demo` by default. Set `REXDIFF_DEMO` for another repository.
It needs fish and python3.

The Lua of the Rex action is in `cmd/rexdiff/rexdiff.lua` and
`cmd/rexdiff/rexdiff_toggle.lua`. The binary embeds both. See [arch.md](arch.md).

Outside Rex, `go run ./cmd/rexdiff` works in any git repository but cannot send
comments.

## Checks

CI runs these checks:

```sh
go vet ./...
go test -race -count=1 ./...
golangci-lint run
go fix -diff ./...
go mod tidy -diff
go run golang.org/x/vuln/cmd/govulncheck@latest ./...
```

On a clean tree, `go fix` and `go mod tidy` print nothing. [test.md](test.md) describes
the tests. To also run them against a real Rex:

```sh
REXDIFF_REX=$(which rex) go test ./test/
```

## Release

```sh
gh release create v0.3.0
```

GitHub Actions runs the tests, builds the binaries, and updates the Homebrew cask.

## Rules

- Write short, plain comments that match the existing code.
- Text from git, Rex, or a pull request can contain escape codes. Clean it with
  `internal/termtext` before you show it.
- Start a program with `internal/proc`, so that its errors have the same form as the
  others.
- `internal/diffview` is copied from crush. Record each change to it in
  `internal/VENDORED.md`.
- When the right behaviour is unclear, follow herdr-reviewr.
- Write commit messages in this form: `fix(tui): keep the view on failed reloads`.
