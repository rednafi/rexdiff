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

	"github.com/rednafi/rexdiff/rex"
)

// The install command adds a Rex action, "Toggle rexdiff", to the command
// palette and binds it to a key. The action opens rexdiff beside the agent
// in focus. If rexdiff runs in the session, the action closes each block
// that runs rexdiff instead. The action finds these blocks by the program
// status that rexdiff reports. A Rex session is the same as a herdr
// workspace.
//
// The action is in two files beside Rex's init.lua, and init.lua loads them
// with one line. rexdiff.lua has the settings of the user. Install writes
// it only if it is not there, and rexdiff does not change it after that.
// rexdiff_toggle.lua has the toggle action. Install and upgrades write
// it whole.

const (
	// requireLine is the line of init.lua that loads rexdiff.lua.
	requireLine = `require("rexdiff")`
	luaFile     = "rexdiff.lua"        // beside init.lua
	toggleFile  = "rexdiff_toggle.lua" // beside init.lua
)

// settingsLua is the content of a new rexdiff.lua: the settings, then the
// loader of rexdiff_toggle.lua. If rexdiff_toggle.lua fails, the loader keeps
// the rest of Rex's config, and the key shows why.
//
//go:embed rexdiff.lua
var settingsLua string

// loaderMark is the first line of the loader in settingsLua.
const loaderMark = "-- rexdiff_toggle.lua has the toggle action"

// oldMark is the line of a rexdiff.lua from rexdiff 0.2.7 above its code.
// The text above the line is the settings of the user.
const oldMark = "-- rexdiff code below. Install and upgrades replace it. Do not edit this line."

// fromOld returns the new rexdiff.lua for old, a rexdiff.lua from rexdiff
// 0.2.7. It keeps the settings of old, and puts the loader in place of the
// code. ok is false if old is not from 0.2.7.
func fromOld(old string) (s string, ok bool) {
	above, _, ok := strings.Cut(old, oldMark)
	if !ok {
		return "", false
	}
	_, loader, _ := strings.Cut(settingsLua, loaderMark)
	return strings.TrimRight(above, "\n") + "\n\n" + loaderMark + loader, true
}

// toggleCode is the code of the toggle action. It needs BIN, the path of
// rexdiff.
//
//go:embed rexdiff_toggle.lua
var toggleCode string

// toggleLua returns the content of rexdiff_toggle.lua for bin, the path of
// rexdiff.
func toggleLua(bin string) string {
	return "-- rexdiff writes this file. Do not edit it. Your settings are in rexdiff.lua.\n" +
		binLine(bin) + "\n" + toggleCode
}

// binLine returns the line of rexdiff_toggle.lua that names bin. upgrade uses
// it to find which rexdiff the file runs.
func binLine(bin string) string { return "local BIN = " + luaQuote(bin) }

// luaQuote returns s as a Lua string. Go's %q writes escapes, such as
// \u200b, that Lua 5.1 does not know.
func luaQuote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`, "\x00", `\0`)
	return `"` + r.Replace(s) + `"`
}

// luaPath returns the path of the file name in the environment env,
// beside the init.lua of Rex.
func luaPath(env []string, name string) string {
	return filepath.Join(filepath.Dir(rex.ConfigPath(env)), name)
}

// readFile returns the content of the file at path, or "" if there is no
// file.
func readFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	return string(b), err
}

// executable returns the path of the rexdiff binary, for the Rex action. It
// keeps a symlink, such as the one from Homebrew, because the target of the
// link changes at each upgrade.
func executable() (string, error) {
	bin, err := os.Executable()
	if err != nil {
		return "", err
	}
	// go run deletes its binary at exit, so the action cannot run it.
	if strings.Contains(bin, string(filepath.Separator)+"go-build") {
		return "", errors.New("install the binary first, for example with go install, then run rexdiff install")
	}
	return bin, nil
}

// install writes rexdiff_toggle.lua with the action that runs bin, the path
// of rexdiff. It writes rexdiff.lua if it is not there, and moves the code
// out of a rexdiff.lua from rexdiff 0.2.7. It adds the require line to
// init.lua. env is the environment of rexdiff.
func install(ctx context.Context, env []string, bin string, args []string, stdout, stderr io.Writer) error {
	if err := parseSub("install", args, env, stderr); err != nil {
		return err
	}

	settingsPath, togglePath := luaPath(env, luaFile), luaPath(env, toggleFile)
	old, err := readFile(settingsPath)
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
	next, fromV027 := fromOld(old)
	switch {
	case old == "":
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
	reload(ctx, env, stdout)
	fmt.Fprintln(stdout, "press the key of the settings (shift+cmd+r by default), or run \"Toggle rexdiff\" from the command palette, beside an agent")
	return nil
}

// upgradeReloadTimeout is the longest time of the reload of the Rex
// config after an upgrade.
const upgradeReloadTimeout = 10 * time.Second

// upgrade writes rexdiff_toggle.lua with the code for bin, if the file is
// there and differs, and reloads Rex's config. A rexdiff.lua from rexdiff
// 0.2.7 gets its code moved to rexdiff_toggle.lua, and keeps its settings.
// It is not the update of rexdiff itself (see the update package). It
// upgrades only the Lua code of the action. It does nothing if the files
// run another rexdiff, for example a build for development. It returns
// what the user must know, or "". env is the environment of rexdiff.
func upgrade(ctx context.Context, env []string, bin string) string {
	settingsPath, togglePath := luaPath(env, luaFile), luaPath(env, toggleFile)
	old, err := readFile(settingsPath)
	if err != nil {
		return ""
	}
	code, err := readFile(togglePath)
	if err != nil {
		return ""
	}
	// Each file names the rexdiff that it runs on this line.
	mine := "\n" + binLine(bin) + "\n"
	next, fromV027 := fromOld(old)
	fromV027 = fromV027 && strings.Contains(old, mine)
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
	// The upgrade runs before the first frame, and the user does not see
	// it. Thus the reload has a deadline.
	ctx, cancel := context.WithCancelCause(ctx)
	ctx, cancelTimeout := context.WithTimeoutCause(ctx, upgradeReloadTimeout,
		fmt.Errorf("rex config reload: %v timeout: %w", upgradeReloadTimeout, context.DeadlineExceeded))
	defer cancelTimeout()
	defer cancel(fmt.Errorf("rex config reload: done: %w", context.Canceled))
	if err := rex.ReloadConfig(ctx, env); err != nil {
		return "upgraded " + toggleFile + "; reload Rex's config yourself: rex config reload"
	}
	return ""
}

// addRequire adds the require line to the end of the init.lua at path. It
// creates the file if it is not there. If the file has the line, addRequire
// does not change it. It reports whether it adds the line.
func addRequire(path string) (bool, error) {
	s, err := readFile(path)
	if err != nil {
		return false, err
	}
	for line := range strings.Lines(s) {
		if isRequire(line) {
			return false, nil
		}
	}
	if s != "" && !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	return true, os.WriteFile(path, []byte(s+requireLine+"\n"), 0o644)
}

// isRequire reports whether line is the require line of rexdiff, maybe
// with a comment after it.
func isRequire(line string) bool {
	t := strings.TrimSpace(line)
	return t == requireLine || strings.HasPrefix(t, requireLine+" --")
}

// parseSub parses the arguments of the subcommand name, which has no
// flags. It returns the errors of parseFlags. env is the environment of
// rexdiff.
func parseSub(name string, args, env []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("rexdiff "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return usageError(err)
	}
	if fs.NArg() > 0 {
		// Show the usage, as the flag package does for a bad flag.
		fmt.Fprintf(stderr, "unknown argument %q (set the key and the placement in the settings in %s)\n",
			fs.Arg(0), luaPath(env, luaFile))
		fs.Usage()
		return errUsage
	}
	return nil
}

// uninstall removes the require line from init.lua, then rexdiff.lua with
// the settings, and rexdiff_toggle.lua. In this order, a failure does not
// leave init.lua with a require of a file that is not there. env is the
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
	reload(ctx, env, stdout)
	return nil
}

// removeRequire removes the require line of rexdiff from the init.lua at
// path. If the file is not there, it does nothing.
func removeRequire(path string) error {
	s, err := readFile(path)
	if err != nil || s == "" {
		return err
	}
	// Split, not Lines, because Join needs the lines without their ends.
	lines := slices.DeleteFunc(strings.Split(s, "\n"), isRequire)
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644)
}

// reload tells Rex to read its configuration file again. rex runs with
// the environment env. If Rex fails, reload tells the user how to reload.
// The reload has no deadline, because the user sees it in a terminal and
// can press Ctrl-C.
func reload(ctx context.Context, env []string, stdout io.Writer) {
	if err := rex.ReloadConfig(ctx, env); err != nil {
		fmt.Fprintf(stdout, "reload Rex's config yourself: rex config reload (%v)\n", err)
		return
	}
	fmt.Fprintln(stdout, "reloaded Rex's config")
}
