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

// init.lua loads rexdiff.lua with the settings. rexdiff.lua loads
// rexdiff_toggle.lua with the action.
const (
	requireLine = `require("rexdiff")`
	luaFile     = "rexdiff.lua"
	toggleFile  = "rexdiff_toggle.lua"
)

// settingsLua is the content of a new rexdiff.lua. If rexdiff_toggle.lua
// fails to load, the rest of the Rex config still works.
//
//go:embed rexdiff.lua
var settingsLua string

const loaderMark = "-- rexdiff_toggle.lua has the toggle action"

// v027Mark separates the settings from the code in a 0.2.7 rexdiff.lua.
const v027Mark = "-- rexdiff code below. Install and upgrades replace it. Do not edit this line."

func migrateV027(oldLua string) (s string, ok bool) {
	above, _, ok := strings.Cut(oldLua, v027Mark)
	if !ok {
		return "", false
	}
	_, loader, _ := strings.Cut(settingsLua, loaderMark)
	return strings.TrimRight(above, "\n") + "\n\n" + loaderMark + loader, true
}

// toggleCode uses the variable BIN for the path of rexdiff.
//
//go:embed rexdiff_toggle.lua
var toggleCode string

func toggleLua(bin string) string {
	return "-- rexdiff writes this file. Do not edit it. Your settings are in rexdiff.lua.\n" +
		binLine(bin) + "\n" + toggleCode
}

func binLine(bin string) string { return "local BIN = " + luaQuote(bin) }

var luaEscapes = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`, "\x00", `\0`)

// luaQuote does not use %q, because Lua 5.1 does not support escapes such
// as \u200b.
func luaQuote(s string) string { return `"` + luaEscapes.Replace(s) + `"` }

func luaPath(env []string, name string) string {
	return filepath.Join(filepath.Dir(rex.ConfigPath(env)), name)
}

// readFile returns "" if the file does not exist.
func readFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	return string(b), err
}

// executablePath does not resolve symbolic links, such as the Homebrew
// link. The link target changes with every upgrade.
func executablePath() (string, error) {
	bin, err := os.Executable()
	if err != nil {
		return "", err
	}
	// go run deletes its binary when it exits.
	if strings.Contains(bin, string(filepath.Separator)+"go-build") {
		return "", errors.New("install the binary first, for example with go install, then run rexdiff install")
	}
	return bin, nil
}

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

const migrateReloadTimeout = 10 * time.Second

// migrateLua does nothing if the files run a different rexdiff binary,
// such as a development build.
func migrateLua(ctx context.Context, env []string, bin string) string {
	settingsPath, togglePath := luaPath(env, luaFile), luaPath(env, toggleFile)
	// The migration is best effort.
	oldLua, err := readFile(settingsPath)
	if err != nil {
		return ""
	}
	code, err := readFile(togglePath)
	if err != nil {
		return ""
	}
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

func isRequireLine(line string) bool {
	t := strings.TrimSpace(line)
	return t == requireLine || strings.HasPrefix(t, requireLine+" --")
}

func parseSub(name string, args, env []string, stderr io.Writer) error {
	flags := flag.NewFlagSet("rexdiff "+name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	if err := flags.Parse(args); err != nil {
		return usageError(err)
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(stderr, "unknown argument %q (set the key and the placement in the settings in %s)\n",
			flags.Arg(0), luaPath(env, luaFile))
		flags.Usage()
		return errUsage
	}
	return nil
}

// uninstall removes the require line first, so that a failure never leaves
// init.lua requiring a lost file.
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

func removeRequire(path string) error {
	s, err := readFile(path)
	if err != nil || s == "" {
		return err
	}
	lines := slices.DeleteFunc(strings.Split(s, "\n"), isRequireLine)
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644)
}

// reloadRex has no deadline, because the user can press Ctrl-C.
func reloadRex(ctx context.Context, env []string, stdout, stderr io.Writer) {
	if err := rex.ReloadConfig(ctx, env); err != nil {
		fmt.Fprintf(stderr, "reload Rex's config yourself: rex config reload (%v)\n", err)
		return
	}
	fmt.Fprintln(stdout, "reloaded Rex's config")
}
