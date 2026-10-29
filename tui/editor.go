package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"unicode"

	tea "charm.land/bubbletea/v2"

	"github.com/rednafi/rexdiff/internal/editor"
	"github.com/rednafi/rexdiff/internal/proc"
	"github.com/rednafi/rexdiff/internal/xctx"
)

// launchEditor opens the file at path on the given line. It uses the
// editor from the settings or, if none is set, $VISUAL, $EDITOR or
// core.editor, read from m.env. The editor runs with m.env. Inside Rex, a
// terminal editor opens in a new block beside rexdiff. Outside Rex, it
// takes over the screen until it exits. A GUI editor starts in the
// background.
func (m *model) launchEditor(path string, line int) tea.Cmd {
	abs := filepath.Join(m.repo.Root(), path)
	sources := editor.Sources{
		Template: m.cfg.Editor,
		Visual:   proc.Getenv(m.env, "VISUAL"),
		Editor:   proc.Getenv(m.env, "EDITOR"),
	}
	if sources.Template == "" {
		ctx, done := m.gitContext("git editor")
		sources.Git = m.repo.GitEditor(ctx)
		done()
	}
	ed, err := editor.CommandFor(sources, abs, line)
	if err != nil {
		m.flash("editor failed: " + err.Error())
		return nil
	}
	// Saving in the editor would create the file again, so do not open a
	// file that no longer exists.
	if info, err := os.Stat(abs); err != nil || !info.Mode().IsRegular() {
		m.flash(path + " is gone")
		return nil
	}
	if _, err := proc.LookPath(m.env, ed.Program); err != nil {
		m.flash(proc.NotInstalled(ed.Program).Error())
		return nil
	}
	m.log.Debug("editor", "command", ed.Script, "window", ed.Window)
	if ed.Window {
		return m.runEditorWindow(path, ed.Script)
	}
	if m.rex == nil {
		return m.runEditorHere(path, ed.Script)
	}
	c, ctx, root := m.rex, m.ctx, m.repo.Root()
	return func() tea.Msg {
		ctx, cancel := xctx.WithTimeout(ctx, rexTimeout, "editor split")
		defer cancel()
		err := c.Split(ctx, root, "sh", "-c", ed.Script)
		return editorPaneMsg{path: path, script: ed.Script, err: err}
	}
}

// onEditorPane handles the result of opening the editor in a new pane. If
// Rex could not open the pane, the editor runs in this pane instead.
func (m *model) onEditorPane(msg editorPaneMsg) tea.Cmd {
	if msg.err == nil {
		m.flash("opened " + msg.path)
		return nil
	}
	m.log.Debug("editor split failed", "err", msg.err)
	return m.runEditorHere(msg.path, msg.script)
}

// runEditorHere runs the editor script in this pane, suspending rexdiff
// until it exits.
func (m *model) runEditorHere(path, script string) tea.Cmd {
	return tea.ExecProcess(command(m.env, "sh", "-c", script), func(err error) tea.Msg {
		return editorDoneMsg{path: path, err: err}
	})
}

// runEditorWindow starts a GUI editor for path with [detach].
func (m *model) runEditorWindow(path, script string) tea.Cmd {
	wait, err := detach(m.env, "editor", "sh", "-c", script)
	if err != nil {
		m.flash("editor failed: " + err.Error())
		return nil
	}
	m.flash("opened " + path)
	return wait
}

// detach starts a program with the environment env in its own session,
// with no controlling terminal. The returned command waits for the program
// to exit. If the program fails, the command returns a detachedMsg that
// includes the last line of its stderr.
func detach(env []string, what, name string, args ...string) (tea.Cmd, error) {
	cmd := command(env, name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return func() tea.Msg {
		err := cmd.Wait()
		if err == nil || errors.Is(err, exec.ErrWaitDelay) {
			return nil
		}
		if last := proc.LastLine(stderr.String()); last != "" {
			err = fmt.Errorf("%s: %w", last, err)
		}
		return detachedMsg{what: what, err: err}
	}, nil
}

// onEditorDone reports how the editor in this pane exited and renders the
// file again, because an editor that exits with an error may still have
// saved it.
func (m *model) onEditorDone(msg editorDoneMsg) {
	exit, isExit := errors.AsType[*exec.ExitError](msg.err)
	switch {
	case msg.err == nil:
		m.flash("edited " + msg.path)
	case isExit:
		m.flash("editor stopped: " + exit.Error())
	default:
		m.flash("editor failed: " + msg.err.Error())
	}
	m.rerender()
}

// urlOpenerCmd returns a command that opens url with the URL opener from
// the settings or, if none is set, the system opener. It accepts only http
// and https URLs. The opener runs with the environment env and without a
// shell. If the opener is not installed, it returns a
// [proc.NotInstalled]. The returned command waits for the opener to
// exit.
func urlOpenerCmd(env []string, cfg config, url string) (tea.Cmd, error) {
	url = strings.TrimSpace(url)
	if !isWebURL(url) {
		return nil, errors.New("unsupported link scheme")
	}
	name, args := "xdg-open", []string{url}
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	if cfg.URLOpener != "" {
		w := openerArgs(cfg.URLOpener, url)
		name, args = w[0], w[1:]
	}
	if _, err := proc.LookPath(env, name); err != nil {
		return nil, proc.NotInstalled(name)
	}
	return detach(env, "open", name, args...)
}

// isWebURL reports whether url is an http or https URL with something
// after the scheme. It rejects URLs that contain control characters or
// bidi marks, because these can hide part of the URL.
func isWebURL(url string) bool {
	lower := strings.ToLower(url)
	web := len(url) > len("http://") && strings.HasPrefix(lower, "http://") ||
		len(url) > len("https://") && strings.HasPrefix(lower, "https://")
	return web && !strings.ContainsFunc(url, func(r rune) bool {
		return unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r)
	})
}

// openerArgs splits spec, the URL opener command, with [splitWords] and
// returns the program followed by its arguments. Each {url} is replaced
// with url. If spec has no {url}, url is added as the last argument.
func openerArgs(spec, url string) []string {
	var args []string
	for _, w := range splitWords(spec) {
		args = append(args, strings.ReplaceAll(w, "{url}", url))
	}
	if !strings.Contains(spec, "{url}") {
		args = append(args, url)
	}
	return args
}

// splitWords splits command into words at white space. Single or double
// quotes keep spaces inside a word, and the quotes themselves are removed.
func splitWords(command string) []string {
	var words []string
	var quote rune
	inWord := false
	for _, r := range command {
		switch {
		case quote != 0 && r == quote:
			quote = 0
		case quote != 0:
			words[len(words)-1] += string(r)
		case unicode.IsSpace(r):
			inWord = false
		default:
			if !inWord {
				words, inWord = append(words, ""), true
			}
			if r == '"' || r == '\'' {
				quote = r
			} else {
				words[len(words)-1] += string(r)
			}
		}
	}
	return words
}

// command returns the command for an editor or URL opener, with the
// environment env. It uses [context.Background] so that the program keeps
// running when rexdiff exits, because killing an editor can lose the
// user's edits.
func command(env []string, name string, args ...string) *exec.Cmd {
	return proc.Command(context.Background(), env, name, args...)
}
