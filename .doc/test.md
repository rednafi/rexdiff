# Tests

![How rexdiff is tested](../.asset/test.svg)

rexdiff has library tests and black-box tests.

## Library tests

- Each library is tested through its public API only, in `package x_test`.
- A test that needs git runs real git in a temporary repository. `internal/testgit`
  hides your git config, so hooks and signing do not change the result.
- A test uses the real limits of a library. A hook in `export_test.go` lowers a limit only
  if the real size makes the test slow.
- A test gives the library its own environment, with fake tools on its own `PATH`. No
  test changes the environment of the process, so all of them run in parallel.

## Black-box tests

- `tui` has no unit tests. The tests in `test/` build the binary and use it as you do.
- Each test types keys and clicks through a terminal emulator, and reads the screen.
- A test waits for the screen or for a file, never for a fixed time.
- Some tests wait for a line in the debug log, for example `msg="turn start"`.
- The scripts in `test/testdata/script` test the commands, for example `install`, with
  testscript.
- Shell scripts stand in for rex, gh, the clipboard, and the editors. The fake rex
  prints output recorded from a real Rex.
- `test/e2e.sh` runs rexdiff in a real Rex, one step at a time. It walks through the user
  guide, hostile file names, tiny and huge blocks, boxes open while the block resizes, and
  a long review. Each step waits until the screen shows a whole frame. The script also
  checks that rex still prints JSON in the shapes that the fake rex records. Run it with
  `REXDIFF_REX=$(which rex) test/e2e.sh`.
- The black-box tests run one at a time, because each one is a real program in a
  terminal.
- The perf tests in `test/perf_test.go` time the program on large repositories. They run
  only if `REXDIFF_PERF` names a directory for the repositories. The libraries have Go
  benchmarks: `go test -bench . ./diff ./vcs ./internal/tree`.
