package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/rednafi/rexdiff/rex"
)

// The install command adds a Rex action, "Toggle rexdiff", to the command
// palette and binds it to a key. The action opens rexdiff beside the agent
// in focus. If rexdiff runs in the session, the action closes each block
// that runs rexdiff instead. The action finds these blocks by the program
// status that rexdiff reports. A Rex session is the same as a herdr
// workspace.
//
// The action is in rexdiff.lua, beside Rex's init.lua, and init.lua loads
// it with one line. rexdiff.lua has the settings of the user at the top and
// the code of rexdiff under codeMark. Install and upgrades replace the code
// and keep the settings.

const (
	// requireLine is the line of init.lua that loads rexdiff.lua.
	requireLine = `require("rexdiff")`
	luaFile     = "rexdiff.lua" // beside init.lua
	// codeMark is the line of rexdiff.lua above the code of rexdiff.
	codeMark = "-- rexdiff code below. Install and upgrades replace it. Do not edit this line."
)

// defaultSettings is the top of a new rexdiff.lua.
const defaultSettings = `-- "rexdiff install" wrote this file. To remove it, run "rexdiff uninstall".
--
-- These are the settings of rexdiff. The README of rexdiff lists them.
-- To change a setting, edit it, then run "rex config reload".
-- Install and upgrades keep this part.
local settings = {
  -- key = "shift+cmd+r", -- the key of the toggle, or false for none
  -- placement = "split", -- split, zoomed, tab, or overlay
  -- layout = "split",
}

`

// codeTemplate is the code of rexdiff.lua, under codeMark. Its %s is the
// line from binLine.
const codeTemplate = `%s
local PLACEMENTS = { split = true, zoomed = true, tab = true, overlay = true }
local TERM = "com.superlogical.terminal"

-- must returns res, or fails the action with err. A Rex call returns nil
-- and an error message when it fails.
local function must(res, err)
  if err ~= nil then error(err, 0) end
  return res
end

-- call calls method on the terminal block with the ID id.
local function call(method, id)
  return must(rex.block.call(TERM, method, { block_id = id }))
end

-- runs_rexdiff returns true if the terminal block id runs rexdiff. rexdiff
-- reports app=rexdiff to Rex with OSC 7501. Rex keeps an idle status after
-- its program stops, so the block counts only while the process that
-- reported the status is in the foreground.
local function runs_rexdiff(id)
  for _, r in ipairs(call("program_status", id).records or {}) do
    if r.app == "rexdiff" and r.owner then
      local fg = call("process", id).foreground
      return fg ~= nil and fg.pid == r.owner.pid
    end
  end
  return false
end

-- reviewers returns the IDs of the blocks in the session that run rexdiff.
local function reviewers()
  local ids = {}
  for _, b in ipairs(must(rex.session.list_blocks{}).blocks or {}) do
    if b.creator_name == TERM and runs_rexdiff(b.block_id) then
      table.insert(ids, b.block_id)
    end
  end
  return ids
end

-- check fails the action if rexdiff does not accept config, the settings
-- as a JSON object. Here the user sees the error. If rexdiff stops at once
-- in a new pane, the pane closes and the error is lost.
local function check(config)
  local r, err = rex.exec({ BIN, "--check", "--config", config })
  if r == nil then error(err, 0) end
  if not r.ok then error((r.stderr:gsub("%%s+$", "")), 0) end
end

-- open_rexdiff opens rexdiff at placement, beside the agent in ctx, with
-- the settings in config. rexdiff starts in the current directory of the
-- agent. The rex package of rexdiff finds the directory the same way.
-- rexdiff starts through the shell of the user, so that it gets the
-- environment of the user, for example EDITOR and PATH.
local function open_rexdiff(ctx, placement, config)
  local p = call("process", ctx.block_id)
  local cwd = (p.foreground or {}).cwd or (p.child or {}).cwd
  local node = rex.layout.block{
    flavor = TERM .. ".shell",
    label = "rexdiff",
    options = {
      command = { BIN, "--agent", ctx.block_id, "--config", config },
      cwd = cwd,
    },
  }
  if placement == "tab" then
    return must(rex.session.new_window{ window_label = "rexdiff", layout = node })
  elseif placement == "overlay" then
    return must(rex.session.new_layer{
      bounds = { 0.04, 0.04, 0.92, 0.92 },
      layout = node,
      focus = true,
    })
  end
  local s = must(rex.session.new_split{
    anchor_id = ctx.block_id,
    direction = "horizontal",
    side = "after",
    ratio = 0.45,
    focus = true,
    layout = node,
  })
  if placement == "zoomed" then
    must(rex.session.zoom_block{ block_id = s.block_ids[1], zoom = true })
  end
  return s
end

-- The key and the placement are for Rex. rexdiff gets the other settings
-- as JSON.
if type(settings) ~= "table" then
  error("rexdiff.lua: settings must be a table, as in: local settings = { }", 0)
end
local opts = {}
for k, v in pairs(settings) do opts[k] = v end
local key = opts.key
if key == nil then key = "shift+cmd+r" end
local default_placement = opts.placement or "split"
opts.key, opts.placement = nil, nil
local config, encode_err = rex.json.encode(opts)

rex.action{
  name = "rexdiff_toggle",
  title = "Toggle rexdiff",
  description = "Review the agent's changes beside it and send comments back",
  category = "rexdiff",
  keywords = "diff review git comments agent",
  args = { placement = "string?" }, -- split, zoomed, tab, or overlay
  run = function(ctx, args)
    local placement = args.placement or default_placement
    if not PLACEMENTS[placement] then
      error("unknown placement " .. tostring(placement)
        .. "; use split, zoomed, tab, or overlay", 0)
    end
    local open = reviewers()
    if #open == 0 then
      if config == nil then error("rexdiff settings: " .. encode_err, 0) end
      check(config)
      return open_rexdiff(ctx, placement, config)
    end
    for _, id in ipairs(open) do
      must(rex.call("block.close", { session_id = ctx.session_id, block_id = id }))
    end
    return { closed = open }
  end,
}
if key then rex.bind(key, "rexdiff_toggle") end
`

// luaCode returns the code part of rexdiff.lua for bin, the path of
// rexdiff, from codeMark to the end.
func luaCode(bin string) string {
	return codeMark + "\n" + fmt.Sprintf(codeTemplate, binLine(bin))
}

// binLine returns the line of rexdiff.lua that names bin. upgrade uses it
// to find which rexdiff the file runs.
func binLine(bin string) string { return "local BIN = " + luaQuote(bin) }

// luaQuote returns s as a Lua string. Go's %q writes escapes, such as
// \u200b, that Lua 5.1 does not know.
func luaQuote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`, "\x00", `\0`)
	return `"` + r.Replace(s) + `"`
}

// splitLua returns the settings part of rexdiff.lua, above codeMark, and the
// code part, from codeMark on. It returns an error if codeMark is gone,
// because then the settings have no end.
func splitLua(path, content string) (settings, code string, err error) {
	i := 0
	for line := range strings.Lines(content) {
		if strings.TrimSpace(line) == codeMark {
			return content[:i], content[i:], nil
		}
		i += len(line)
	}
	return "", "", fmt.Errorf("%s has no line %q, so rexdiff cannot tell your "+
		"settings from its code. Put that line back under your settings, or move "+
		"the file away and run \"rexdiff install\" again", path, codeMark)
}

// newLua returns the content of rexdiff.lua with the code for bin. It
// keeps the settings of old, the content that the file has now. If old is
// empty, it uses the default settings.
func newLua(path, old, bin string) (string, error) {
	if old == "" {
		return defaultSettings + luaCode(bin), nil
	}
	settings, _, err := splitLua(path, old)
	return settings + luaCode(bin), err
}

func luaPath() string { return filepath.Join(filepath.Dir(rex.ConfigPath()), luaFile) }

// readFile returns the content of the file at path, or "" if there is no
// file.
func readFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	return string(b), err
}

// executable returns the path of the rexdiff binary, for the Rex action. It
// keeps a symlink, such as the one from Homebrew. The target of the link
// changes at each upgrade.
func executable() (string, error) {
	bin, err := os.Executable()
	if err != nil {
		return "", err
	}
	// go run deletes its binary at exit, so the action cannot run it.
	if strings.Contains(bin, string(filepath.Separator)+"go-build") {
		return "", errors.New("install the binary first, for example with go install, " +
			"then run rexdiff install")
	}
	return bin, nil
}

// install writes rexdiff.lua with the action that runs bin, the path of
// rexdiff. It adds the require line to init.lua.
func install(
	ctx context.Context,
	bin string,
	args []string,
	stdout, stderr io.Writer,
) error {
	fs := flag.NewFlagSet("rexdiff install", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unknown argument %s (set the key and the placement "+
			"in the settings in %s)", fs.Arg(0), luaPath())
	}

	// Make the new content first. Then a bad rexdiff.lua stops the install
	// before it changes a file.
	lua := luaPath()
	old, err := readFile(lua)
	if err != nil {
		return err
	}
	code, err := newLua(lua, old, bin)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(lua), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(lua, []byte(code), 0o644); err != nil {
		return err
	}
	initPath := rex.ConfigPath()
	if err := addRequire(initPath); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "wrote %s; the settings are at its top\n", lua)
	fmt.Fprintf(stdout, "added %s to %s\n", requireLine, initPath)
	fmt.Fprint(stdout, reload(ctx, stdout))
	fmt.Fprintln(stdout, "press the key of the settings (shift+cmd+r by default), "+
		"or run \"Toggle rexdiff\" from the command palette, beside an agent")
	return nil
}

// upgrade replaces the code part of rexdiff.lua with the code for bin, if
// they differ, and keeps the settings. It does nothing if there is no
// rexdiff.lua, or if the file runs another rexdiff, for example a build
// for development. It returns what the user must know, or "".
func upgrade(ctx context.Context, bin string) string {
	lua := luaPath()
	old, err := readFile(lua)
	if err != nil || !strings.Contains(old, binLine(bin)+"\n") {
		return ""
	}
	code, err := newLua(lua, old, bin)
	if err != nil {
		return "cannot upgrade rexdiff.lua: " + err.Error()
	}
	if code == old {
		return ""
	}
	if err := os.WriteFile(lua, []byte(code), 0o644); err != nil {
		return "cannot upgrade rexdiff.lua: " + err.Error()
	}
	if _, err := rex.ReloadConfig(ctx); err != nil {
		return "upgraded rexdiff.lua; reload Rex's config yourself: rex config reload"
	}
	return ""
}

// addRequire adds the require line to the end of the init.lua at path. It
// creates the file if it is not there. If the file has the line, addRequire
// does not change it.
func addRequire(path string) error {
	s, err := readFile(path)
	if err != nil {
		return err
	}
	if slices.ContainsFunc(strings.Split(s, "\n"), isRequire) {
		return nil
	}
	if s != "" && !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	return os.WriteFile(path, []byte(s+requireLine+"\n"), 0o644)
}

// isRequire reports whether line is the require line of rexdiff, maybe
// with a comment after it.
func isRequire(line string) bool {
	t := strings.TrimSpace(line)
	return t == requireLine || strings.HasPrefix(t, requireLine+" --")
}

// uninstall removes the require line from init.lua, then rexdiff.lua with
// the settings. In this order, a failure does not leave init.lua with a
// require of a file that is not there.
func uninstall(ctx context.Context, stdout io.Writer) error {
	initPath := rex.ConfigPath()
	if err := removeRequire(initPath); err != nil {
		return err
	}
	if err := os.Remove(luaPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	fmt.Fprintln(stdout, "removed the rexdiff action, its key, and its settings from Rex")
	reload(ctx, stdout)
	return nil
}

// removeRequire removes the require line of rexdiff from the init.lua at
// path. If the file is not there, it does nothing.
func removeRequire(path string) error {
	s, err := readFile(path)
	if err != nil || s == "" {
		return err
	}
	lines := slices.DeleteFunc(strings.Split(s, "\n"), isRequire)
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644)
}

// reload tells Rex to read its configuration file again, and returns the
// report of Rex. If Rex fails, reload tells the user to reload by hand and
// returns "".
func reload(ctx context.Context, stdout io.Writer) string {
	out, err := rex.ReloadConfig(ctx)
	if err != nil {
		fmt.Fprintf(stdout, "reload Rex's config yourself: rex config reload (%v)\n", err)
		return ""
	}
	return out
}
