// Package editor chooses the user's text editor and builds the sh command
// that opens a file at a given line. It also reports whether the editor
// opens its own window or runs in the terminal.
package editor

import (
	"cmp"
	"errors"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
)

// Command is a command that opens a file at a given line.
type Command struct {
	Script  string // the command, for sh -c
	Program string // the first word of the command, or "" if there is none
	Window  bool   // the editor opens its own window instead of running in the terminal
}

// Sources holds the places that can name the editor, in the order that
// CommandFor tries them.
type Sources struct {
	Template string // the editor command from the settings
	Visual   string // $VISUAL
	Editor   string // $EDITOR
	Git      string // core.editor
}

// CommandFor returns the command that opens file at line. It uses
// s.Template if it is not empty. Otherwise it uses the first non-blank
// value of s.Visual, s.Editor and s.Git. The error messages are written
// for the user.
func CommandFor(s Sources, file string, line int) (Command, error) {
	var c Command
	if s.Template != "" {
		c = fromTemplate(s.Template, file, line)
	} else {
		e := cmp.Or(strings.TrimSpace(s.Visual), strings.TrimSpace(s.Editor), strings.TrimSpace(s.Git))
		if e == "" {
			return Command{}, errors.New(`no editor: set "editor" in rexdiff.lua, $EDITOR, or git's core.editor`)
		}
		c = fromEditor(e, file, line)
	}
	if c.Program == "" {
		return Command{}, errors.New("the editor command names no program")
	}
	return c, nil
}

// fromTemplate returns the command that opens file at line using the
// user's command template. It replaces {file} and {line}. If template has
// no {file}, the file is added at the end.
func fromTemplate(template, file string, line int) Command {
	r := strings.NewReplacer("{file}", shellQuote(file), "{line}", strconv.Itoa(line))
	script := r.Replace(template)
	if !strings.Contains(template, "{file}") {
		script += " " + shellQuote(file)
	}
	return Command{Script: script, Program: program(template), Window: dialectOf(template).window}
}

// fromEditor returns the command that opens file at line using an editor
// command such as $EDITOR. It adds the file and line in the form that the
// editor expects.
func fromEditor(command, file string, line int) Command {
	d := dialectOf(command)
	n, f := strconv.Itoa(line), shellQuote(file)
	at := shellQuote(file + ":" + n)
	var args string
	switch d.line {
	case argNone:
		args = f
	case argPlus:
		args = "+" + n + " " + f
	case argSuffix:
		args = at
	case argGoto:
		args = "-g " + at
	case argFlag:
		args = "--line " + n + " " + f
	}
	return Command{Script: command + " " + args, Program: program(command), Window: d.window}
}

// shellQuote quotes s as a single word for sh.
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// lineArg is the way an editor accepts the line number to open at.
type lineArg int

const (
	argNone   lineArg = iota // file
	argPlus                  // +42 file
	argSuffix                // file:42
	argGoto                  // -g file:42
	argFlag                  // --line 42 file
)

// dialect describes how an editor accepts the line number, and whether it
// opens a window.
type dialect struct {
	line   lineArg
	window bool
}

// dialects maps the program names of known editors to their dialects.
var dialects = knownDialects()

// knownDialects builds the dialects table.
func knownDialects() map[string]dialect {
	d := map[string]dialect{}
	add := func(dl dialect, names ...string) {
		for _, n := range names {
			d[n] = dl
		}
	}
	add(dialect{argPlus, false}, "vi", "vim", "nvim", "lvim", "vis", "joe", "nano", "micro", "kak")
	// Emacs may or may not open a window. Both work in a pane.
	add(dialect{argPlus, false}, "emacs", "emacsclient")
	add(dialect{argSuffix, false}, "hx", "helix", "fresh")
	// MacVim, gVim, BBEdit and gedit open a window.
	add(dialect{argPlus, true}, "mvim", "gvim", "bbedit", "gedit")
	add(dialect{argGoto, true},
		"code", "code-insiders", "codium", "vscodium", "cursor", "windsurf", "positron")
	add(dialect{argSuffix, true}, "subl", "sublime_text", "zed", "zeditor", "zedit")
	add(dialect{argFlag, true}, "mate", "kate", "idea", "pycharm", "webstorm", "goland",
		"clion", "phpstorm", "rubymine", "rider", "datagrip", "rustrover", "dataspell", "fleet")
	add(dialect{argNone, true}, "open", "xdg-open")
	if runtime.GOOS == "darwin" {
		add(dialect{argFlag, true}, "xed") // the Xcode command-line launcher
	}
	return d
}

// dialectOf returns the dialect for the program in command. An unknown
// editor is assumed to accept +line and run in the terminal. A --wait or
// --block flag marks an editor that opens a window.
func dialectOf(command string) dialect {
	d := dialect{line: argPlus}
	if known, ok := dialects[filepath.Base(program(command))]; ok {
		d = known
	}
	words := strings.Fields(command)
	if slices.Contains(words, "--wait") || slices.Contains(words, "--block") {
		d.window = true
	}
	return d
}

// program returns the first word of command. A quoted word can contain
// spaces, for example "/Applications/Visual Studio Code.app/.../code".
func program(command string) string {
	command = strings.TrimSpace(command)
	for _, q := range []string{`"`, "'"} {
		if rest, ok := strings.CutPrefix(command, q); ok {
			word, _, _ := strings.Cut(rest, q)
			return word
		}
	}
	word, _, _ := strings.Cut(command, " ")
	return word
}
