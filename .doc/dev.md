# Development

You need Go at the version in `go.mod`, git, and golangci-lint v2.15.0:

```sh
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0
```

## Run a change in Rex

```sh
./dev.sh
```

This builds rexdiff and opens it beside an "agent" pane in the Rex session
`rexreview-test`. Run it again after each change. The log is in `/tmp/rexdiff.log`. Set
`REXDIFF_DEMO` to show another repository.

Outside Rex, `go run ./cmd/rexdiff` works in any git repository, but cannot send.

## Check

```sh
go vet ./...
go test -race -count=1 ./...
golangci-lint run
go fix -diff ./...
go mod tidy -diff
go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...
```

To run the tests against a real Rex too:

```sh
REXDIFF_REX=$(which rex) go test ./test/
```

## Release

```sh
gh release create v0.3.0
```

GitHub Actions builds the binaries and updates the Homebrew cask.

## Rules

- Clean text from git, Rex, or a pull request with `internal/termtext` before you show
  it.
- Start a program with `internal/proc`.
- Keep the crush notice in each file that `internal/DERIVED.md` lists.
- Write commit messages in this form: `fix(tui): keep the view on failed reloads`.
