// Package editor builds the sh command that opens a file at a line in the
// user's editor.
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
	Script  string // for sh -c
	Program string
	Window  bool // the editor opens its own window, not in the terminal
}

// Sources holds the places that can name the editor, in the order that
// CommandFor tries them.
type Sources struct {
	Template string // from the settings
	Visual   string
	Editor   string
	Git      string // core.editor
}

// CommandFor returns the command that opens file at line. The error
// messages are for the user.
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

// fromTemplate quotes the file for the quotes around {file}, because in
// "…" or '…' the quotes of shellQuote are literal, and sh then runs a
// $(…) in the name.
func fromTemplate(template, file string, line int) Command {
	var b strings.Builder
	var open byte // the open quote, or 0
	for i := 0; i < len(template); i++ {
		switch c, rest := template[i], template[i:]; {
		case strings.HasPrefix(rest, "{file}"):
			b.WriteString(quoteIn(open, file))
			i += len("{file}") - 1
		case strings.HasPrefix(rest, "{line}"):
			b.WriteString(strconv.Itoa(line))
			i += len("{line}") - 1
		case c == '\\' && open != '\'' && i+1 < len(template):
			b.WriteString(template[i : i+2])
			i++
		default:
			b.WriteByte(c)
			switch {
			case open == 0 && (c == '\'' || c == '"'):
				open = c
			case open == c:
				open = 0
			}
		}
	}
	script := b.String()
	if !strings.Contains(template, "{file}") {
		script += " " + shellQuote(file)
	}
	return Command{Script: script, Program: program(template), Window: dialectOf(template).window}
}

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

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// quoteIn quotes s for sh inside the quote open, or outside quotes if open
// is 0.
func quoteIn(open byte, s string) string {
	switch open {
	case '\'':
		return strings.ReplaceAll(s, "'", `'\''`)
	case '"':
		return inDoubleQuotes.Replace(s)
	}
	return shellQuote(s)
}

// In "…", a backslash makes only these characters literal.
var inDoubleQuotes = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "$", `\$`, "`", "\\`")

type lineArg int

const (
	argNone   lineArg = iota // file
	argPlus                  // +42 file
	argSuffix                // file:42
	argGoto                  // -g file:42
	argFlag                  // --line 42 file
)

type dialect struct {
	line   lineArg
	window bool
}

var dialects = knownDialects()

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
