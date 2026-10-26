// Package editor picks the text editor of the user, and makes the sh
// command that opens a file at a line in it. It also reports whether the
// editor opens a window or draws in the terminal.
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

// Command is a command that opens a file at a line.
type Command struct {
	Script  string // the command, for sh -c
	Program string // the first word of the command, or "" if it has none
	Window  bool   // the editor opens a window, not a terminal editor
}

// Sources are the places that can name the editor, in the order that
// Resolve tries them.
type Sources struct {
	Template string // the editor command of the settings
	Visual   string // $VISUAL
	Editor   string // $EDITOR
	Git      string // core.editor
}

// Resolve returns the command that opens file at line. It uses
// s.Template if it is not empty. Else it uses the first of s.Visual,
// s.Editor, and s.Git that is not blank. Each error is a message for the
// user.
func Resolve(s Sources, file string, line int) (Command, error) {
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

// fromTemplate returns the command that opens file at line with template,
// a command that the user writes. The template gets file in place of
// {file}, and line in place of {line}. Without {file}, file goes at the
// end.
func fromTemplate(template, file string, line int) Command {
	r := strings.NewReplacer("{file}", shellQuote(file), "{line}", strconv.Itoa(line))
	script := r.Replace(template)
	if !strings.Contains(template, "{file}") {
		script += " " + shellQuote(file)
	}
	return Command{Script: script, Program: program(template), Window: dialectOf(template).window}
}

// fromEditor returns the command that opens file at line in an editor.
// command starts the editor, for example the value of $EDITOR. fromEditor
// adds file and line in the form that the editor expects.
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

// shellQuote quotes s as one word for sh.
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// lineArg is how an editor takes the line to open at.
type lineArg int

const (
	argNone   lineArg = iota // file
	argPlus                  // +42 file
	argSuffix                // file:42
	argGoto                  // -g file:42
	argFlag                  // --line 42 file
)

// dialect tells how an editor takes the line, and if it opens a window.
type dialect struct {
	line   lineArg
	window bool
}

// dialects has the known editors, by program name. The table is the same
// as in herdr-reviewr. The table also lists fresh, open, and xdg-open.
var dialects = knownDialects()

// knownDialects returns the table of dialects.
func knownDialects() map[string]dialect {
	d := map[string]dialect{}
	add := func(dl dialect, names ...string) {
		for _, n := range names {
			d[n] = dl
		}
	}
	add(dialect{argPlus, false}, "vi", "vim", "nvim", "lvim", "vis", "joe", "nano", "micro", "kak")
	// Emacs can open a window or not. In a pane, it works in both cases.
	add(dialect{argPlus, false}, "emacs", "emacsclient")
	add(dialect{argSuffix, false}, "hx", "helix", "fresh")
	// MacVim and gVim open a window, unlike the other vi editors.
	add(dialect{argPlus, true}, "mvim", "gvim", "bbedit", "gedit")
	add(dialect{argGoto, true},
		"code", "code-insiders", "codium", "vscodium", "cursor", "windsurf", "positron")
	add(dialect{argSuffix, true}, "subl", "sublime_text", "zed", "zeditor", "zedit")
	add(dialect{argFlag, true}, "mate", "kate", "idea", "pycharm", "webstorm", "goland",
		"clion", "phpstorm", "rubymine", "rider", "datagrip", "rustrover", "dataspell", "fleet")
	add(dialect{argNone, true}, "open", "xdg-open")
	if runtime.GOOS == "darwin" {
		add(dialect{argFlag, true}, "xed") // the opener of Xcode
	}
	return d
}

// dialectOf returns the dialect of the program of command. An unknown
// editor takes +line and draws in the terminal. Most terminal editors take
// +line. A window editor also works in a pane. A wait flag tells that the
// editor has a window.
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

// program returns the first word of command. A word in quotes can have
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
