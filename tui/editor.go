package tui

import (
	"bytes"
	"cmp"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	tea "charm.land/bubbletea/v2"
)

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
// An editor without a window draws in the terminal, so it needs a pane.
type dialect struct {
	line   lineArg
	window bool
}

// dialects has the editors that rexdiff knows, by program name. The table
// is the one of herdr-reviewr, with fresh added.
var dialects = map[string]dialect{}

func init() {
	add := func(d dialect, names ...string) {
		for _, n := range names {
			dialects[n] = d
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
	if runtime.GOOS == "darwin" {
		add(dialect{argFlag, true}, "xed") // the opener of Xcode
	}
}

// editorCommand is a command that opens a file at a line.
type editorCommand struct {
	script   string // the command, for sh -c
	terminal bool   // the editor draws in the terminal
}

// resolveEditor returns the command that opens the file abs at line. It
// uses template, the editor of the settings, else value, the editor of
// the environment or of git. A template gets abs in place of {file} and
// line in place of {line}. For value, rexdiff adds abs and line as the
// editor expects them.
func resolveEditor(template, value, abs string, line int) editorCommand {
	if template != "" {
		r := strings.NewReplacer("{file}", shellQuote(abs), "{line}", strconv.Itoa(line))
		script := r.Replace(template)
		if !strings.Contains(template, "{file}") {
			script += " " + shellQuote(abs)
		}
		return editorCommand{script, wantsTerminal(template)}
	}
	n, file := strconv.Itoa(line), shellQuote(abs)
	var args string
	switch dialects[program(value)].line {
	case argNone:
		args = file
	case argPlus:
		args = "+" + n + " " + file
	case argSuffix:
		args = shellQuote(abs + ":" + n)
	case argGoto:
		args = "-g " + shellQuote(abs+":"+n)
	case argFlag:
		args = "--line " + n + " " + file
	}
	return editorCommand{value + " " + args, wantsTerminal(value)}
}

// program returns the name of the program of the command, without its
// directory.
func program(command string) string {
	words := strings.Fields(command)
	if len(words) == 0 {
		return ""
	}
	return filepath.Base(words[0])
}

// wantsTerminal reports whether the command draws in the terminal. A wait
// flag tells that the editor has a window. An editor that rexdiff does not
// know draws in the terminal, because in a pane a window editor also works.
func wantsTerminal(command string) bool {
	for _, w := range strings.Fields(command) {
		if w == "--wait" || w == "--block" {
			return false
		}
	}
	d, ok := dialects[program(command)]
	return !ok || !d.window
}

// openEditor opens the file at path, at line. It uses the editor of the
// settings, else $VISUAL, $EDITOR, core.editor, or vi. A terminal editor
// opens in a new block beside rexdiff in Rex. Outside Rex, it takes the
// screen until it stops. A window editor starts in the background.
func (m *model) openEditor(path string, line int) tea.Cmd {
	abs := filepath.Join(m.repo.Root(), path)
	value := ""
	if m.cfg.Editor == "" {
		value = cmp.Or(
			strings.TrimSpace(os.Getenv("VISUAL")),
			strings.TrimSpace(os.Getenv("EDITOR")),
			strings.TrimSpace(m.repo.Editor(m.ctx)),
			"vi",
		)
	}
	ed := resolveEditor(m.cfg.Editor, value, abs, line)
	m.log.Debug("editor", "command", ed.script, "terminal", ed.terminal)
	if !ed.terminal {
		return runWindowEditor(ed.script)
	}
	if m.rex != nil {
		err := m.rex.Split(m.ctx, m.repo.Root(), "sh", "-c", ed.script)
		if err == nil {
			return nil
		}
		m.log.Debug("editor split", "err", err)
	}
	done := func(err error) tea.Msg { return editorDoneMsg{err} }
	return tea.ExecProcess(exec.Command("sh", "-c", ed.script), done)
}

// runWindowEditor starts a window editor. The editor gets its own session
// and no terminal, so that it cannot read the keys of rexdiff. The command
// reports an error if the editor fails, with the last line of its stderr.
func runWindowEditor(script string) tea.Cmd {
	cmd := exec.Command("sh", "-c", script)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return func() tea.Msg { return editorDoneMsg{err} }
	}
	return func() tea.Msg {
		err := cmd.Wait()
		if err == nil {
			return nil
		}
		lines := strings.Split(strings.TrimSpace(stderr.String()), "\n")
		if last := lines[len(lines)-1]; last != "" {
			err = fmt.Errorf("%w: %s", err, last)
		}
		return editorDoneMsg{err}
	}
}

// openURL opens url with the URL opener of the settings, else with
// the opener of the system.
func openURL(cfg config, url string) error {
	if cfg.URLOpener != "" {
		script := strings.ReplaceAll(cfg.URLOpener, "{url}", shellQuote(url))
		return start("sh", "-c", script)
	}
	opener := "xdg-open"
	if runtime.GOOS == "darwin" {
		opener = "open"
	}
	return start(opener, url)
}

// start starts a program and does not wait for it to stop. A goroutine
// waits for it, so that it does not stay a zombie process.
func start(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}

// shellQuote quotes s as one word for sh.
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
