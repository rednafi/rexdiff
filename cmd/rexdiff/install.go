package main

import (
	"context"
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/rednafi/rexdiff/internal/xctx"
	"github.com/rednafi/rexdiff/rex"
)

// The require line in init.lua loads rexdiff.lua, which holds the user's
// settings and loads rexdiff_toggle.lua, which holds the action.
const (
	// requireLine is the line in init.lua that loads rexdiff.lua.
	requireLine = `require("rexdiff")`
	luaFile     = "rexdiff.lua"        // in the same directory as init.lua
	toggleFile  = "rexdiff_toggle.lua" // in the same directory as init.lua
)

// settingsLua is the content of a new rexdiff.lua: the settings, followed
// by code that loads rexdiff_toggle.lua. If rexdiff_toggle.lua fails to
// load, the rest of the Rex config still works, and pressing the key shows
// the error.
//
//go:embed rexdiff.lua
var settingsLua string

// loaderMark is the first line of the loading code in settingsLua.
const loaderMark = "-- rexdiff_toggle.lua has the toggle action"

// v027Mark is the line that separates the user's settings, above it, from
// the action code, below it, in a rexdiff.lua written by rexdiff 0.2.7.
const v027Mark = "-- rexdiff code below. Install and upgrades replace it. Do not edit this line."

// migrateV027 converts oldLua, a rexdiff.lua written by rexdiff 0.2.7, to
// the current format. It keeps the settings and replaces the action code
// with the code that loads rexdiff_toggle.lua. ok is false if oldLua was
// not written by 0.2.7.
func migrateV027(oldLua string) (s string, ok bool) {
	above, _, ok := strings.Cut(oldLua, v027Mark)
	if !ok {
		return "", false
	}
	_, loader, _ := strings.Cut(settingsLua, loaderMark)
	return strings.TrimRight(above, "\n") + "\n\n" + loaderMark + loader, true
}

// toggleCode is the Lua code of the toggle action. It uses the variable
// BIN for the path of rexdiff.
//
//go:embed rexdiff_toggle.lua
var toggleCode string

// toggleLua returns the content of rexdiff_toggle.lua for the rexdiff
// binary at bin.
func toggleLua(bin string) string {
	return "-- rexdiff writes this file. Do not edit it. Your settings are in rexdiff.lua.\n" +
		binLine(bin) + "\n" + toggleCode
}

// binLine returns the line of rexdiff_toggle.lua that sets BIN to bin.
func binLine(bin string) string { return "local BIN = " + luaQuote(bin) }

// luaEscapes escapes the characters that cannot appear unescaped in a Lua
// string literal.
var luaEscapes = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`, "\x00", `\0`)

// luaQuote returns s as a Lua string literal. It does not use %q, because
// Lua 5.1 does not support escapes such as \u200b.
func luaQuote(s string) string { return `"` + luaEscapes.Replace(s) + `"` }

// luaPath returns the path of the file called name in the directory of
// the Rex init.lua for env.
func luaPath(env []string, name string) string {
	return filepath.Join(filepath.Dir(rex.ConfigPath(env)), name)
}

// readFile returns the content of the file at path, or "" if the file does
// not exist.
func readFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	return string(b), err
}

// executablePath returns the path of the rexdiff binary for the Rex action
// to run. It does not resolve symbolic links, such as the one Homebrew
// creates, because the link target changes with every upgrade.
func executablePath() (string, error) {
	bin, err := os.Executable()
	if err != nil {
		return "", err
	}
	// go run deletes its binary when it exits, so the action could not run
	// it later.
	if strings.Contains(bin, string(filepath.Separator)+"go-build") {
		return "", errors.New("install the binary first, for example with go install, then run rexdiff install")
	}
	return bin, nil
}

// install writes rexdiff_toggle.lua with the Rex action "Toggle rexdiff",
// bound to a key, which opens bin next to the focused agent or, if
// rexdiff is already running in the session, closes every rexdiff block.
// It writes rexdiff.lua if it does not exist, migrates a rexdiff.lua from
// 0.2.7, adds the require line to init.lua and reloads the Rex config.
// env is the environment of rexdiff.
func install(ctx context.Context, env []string, bin string, args []string, stdout, stderr io.Writer) error {
	if err := parseSub("install", args, env, stderr); err != nil {
		return err
	}

	settingsPath, togglePath := luaPath(env, luaFile), luaPath(env, toggleFile)
	oldLua, err := readFile(settingsPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(togglePath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(togglePath, []byte(toggleLua(bin)), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "wrote %s\n", togglePath)
	next, fromV027 := migrateV027(oldLua)
	switch {
	case oldLua == "":
		next = settingsLua
		fmt.Fprintf(stdout, "wrote %s with your settings\n", settingsPath)
	case fromV027:
		fmt.Fprintf(stdout, "kept your settings in %s, and moved the code out\n", settingsPath)
	default:
		fmt.Fprintf(stdout, "kept your settings in %s\n", settingsPath)
	}
	if next != "" {
		if err := os.WriteFile(settingsPath, []byte(next), 0o644); err != nil {
			return err
		}
	}
	initPath := rex.ConfigPath(env)
	added, err := addRequire(initPath)
	if err != nil {
		return err
	}
	if added {
		fmt.Fprintf(stdout, "added %s to %s\n", requireLine, initPath)
	}
	reloadRex(ctx, env, stdout, stderr)
	fmt.Fprintln(stdout, "press the key of the settings (shift+cmd+r by default), or run \"Toggle rexdiff\" from the command palette, beside an agent")
	return nil
}

// migrateReloadTimeout limits how long migrateLua waits for Rex to reload
// its config.
const migrateReloadTimeout = 10 * time.Second

// migrateLua rewrites rexdiff_toggle.lua with the current code for bin if
// the file exists and is out of date, migrates a rexdiff.lua from 0.2.7,
// and then reloads the Rex config. It does nothing if the files run a
// different rexdiff binary, for example a development build. It returns a
// message for the user, or "". env is the environment of rexdiff.
func migrateLua(ctx context.Context, env []string, bin string) string {
	settingsPath, togglePath := luaPath(env, luaFile), luaPath(env, toggleFile)
	// The migration is best effort: if a file cannot be read, rexdiff
	// starts as usual and leaves the files as they are.
	oldLua, err := readFile(settingsPath)
	if err != nil {
		return ""
	}
	code, err := readFile(togglePath)
	if err != nil {
		return ""
	}
	// Each file contains this line, which names the rexdiff binary that it
	// runs.
	mine := "\n" + binLine(bin) + "\n"
	next, fromV027 := migrateV027(oldLua)
	fromV027 = fromV027 && strings.Contains(oldLua, mine)
	want := toggleLua(bin)
	if !fromV027 && (code == want || !strings.Contains(code, mine)) {
		return ""
	}
	if err := os.WriteFile(togglePath, []byte(want), 0o644); err != nil {
		return toggleFile + " upgrade failed: " + err.Error()
	}
	if fromV027 {
		if err := os.WriteFile(settingsPath, []byte(next), 0o644); err != nil {
			return luaFile + " upgrade failed: " + err.Error()
		}
	}
	// migrateLua runs before rexdiff draws its first frame, so the user
	// cannot see or cancel it. Give the reload a deadline.
	ctx, cancel := xctx.WithTimeout(ctx, migrateReloadTimeout, "rex config reload")
	defer cancel()
	if err := rex.ReloadConfig(ctx, env); err != nil {
		return "upgraded " + toggleFile + ". Reload Rex's config yourself: rex config reload"
	}
	return ""
}

// addRequire appends the require line to the init.lua at path, creating
// the file if needed. It does nothing if the line is already there, and
// reports whether it added the line.
func addRequire(path string) (bool, error) {
	s, err := readFile(path)
	if err != nil {
		return false, err
	}
	for line := range strings.Lines(s) {
		if isRequireLine(line) {
			return false, nil
		}
	}
	if s != "" && !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	return true, os.WriteFile(path, []byte(s+requireLine+"\n"), 0o644)
}

// isRequireLine reports whether line is rexdiff's require line, possibly
// followed by a Lua comment.
func isRequireLine(line string) bool {
	t := strings.TrimSpace(line)
	return t == requireLine || strings.HasPrefix(t, requireLine+" --")
}

// parseSub parses the arguments of the subcommand name, which takes no
// flags or arguments. For -h it returns [flag.ErrHelp]. For an unknown
// flag or any argument it returns errUsage. env is the environment of
// rexdiff.
func parseSub(name string, args, env []string, stderr io.Writer) error {
	flags := flag.NewFlagSet("rexdiff "+name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	if err := flags.Parse(args); err != nil {
		return usageError(err)
	}
	if flags.NArg() > 0 {
		// Print the usage, as the flag package does for an unknown flag.
		fmt.Fprintf(stderr, "unknown argument %q (set the key and the placement in the settings in %s)\n",
			flags.Arg(0), luaPath(env, luaFile))
		flags.Usage()
		return errUsage
	}
	return nil
}

// uninstall removes the require line from init.lua, then deletes
// rexdiff.lua and rexdiff_toggle.lua. Because of this order, a failure
// never leaves init.lua requiring a file that no longer exists. env is the
// environment of rexdiff.
func uninstall(ctx context.Context, env, args []string, stdout, stderr io.Writer) error {
	if err := parseSub("uninstall", args, env, stderr); err != nil {
		return err
	}
	initPath := rex.ConfigPath(env)
	if err := removeRequire(initPath); err != nil {
		return err
	}
	for _, name := range []string{luaFile, toggleFile} {
		if err := os.Remove(luaPath(env, name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	fmt.Fprintln(stdout, "removed the rexdiff action, its key, and its settings from Rex")
	reloadRex(ctx, env, stdout, stderr)
	return nil
}

// removeRequire removes rexdiff's require line from the init.lua at path.
// If the file does not exist, it does nothing.
func removeRequire(path string) error {
	s, err := readFile(path)
	if err != nil || s == "" {
		return err
	}
	// Use Split, not Lines, because Join needs lines without their
	// newlines.
	lines := slices.DeleteFunc(strings.Split(s, "\n"), isRequireLine)
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644)
}

// reloadRex runs rex with env to make Rex read its configuration file
// again. If that fails, reloadRex tells the user on stderr how to reload
// by hand. The reload has no deadline, because the user can press Ctrl-C.
func reloadRex(ctx context.Context, env []string, stdout, stderr io.Writer) {
	if err := rex.ReloadConfig(ctx, env); err != nil {
		fmt.Fprintf(stderr, "reload Rex's config yourself: rex config reload (%v)\n", err)
		return
	}
	fmt.Fprintln(stdout, "reloaded Rex's config")
}
