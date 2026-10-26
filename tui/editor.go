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
)

// openEditor opens the file at path, at line. It uses the editor of the
// settings, else $VISUAL, $EDITOR, or core.editor. The variables come from
// m.env. The editor runs with m.env. A terminal editor opens in a new
// block beside rexdiff in Rex. Outside Rex, a terminal editor takes the
// screen until the editor stops. A window editor starts in the background.
// The messages are the same as in herdr-reviewr.
func (m *model) openEditor(path string, line int) tea.Cmd {
	abs := filepath.Join(m.repo.Root(), path)
	src := editor.Sources{
		Template: m.cfg.Editor,
		Visual:   proc.Getenv(m.env, "VISUAL"),
		Editor:   proc.Getenv(m.env, "EDITOR"),
	}
	if src.Template == "" {
		src.Git = m.repo.Editor(m.ctx)
	}
	ed, err := editor.Resolve(src, abs, line)
	if err != nil {
		m.flash("editor failed: " + err.Error())
		return nil
	}
	// The editor makes the file again when it saves, so do not open a
	// file that is not on disk.
	if fi, err := os.Stat(abs); err != nil || !fi.Mode().IsRegular() {
		m.flash(path + " is gone")
		return nil
	}
	if _, err := proc.LookPath(m.env, ed.Program); err != nil {
		m.flash(proc.NotInstalled(ed.Program).Error())
		return nil
	}
	m.log.Debug("editor", "command", ed.Script, "window", ed.Window)
	if ed.Window {
		return m.runWindowEditor(path, ed.Script)
	}
	if m.rex == nil {
		return m.runHere(path, ed.Script)
	}
	c, ctx, root := m.rex, m.ctx, m.repo.Root()
	return func() tea.Msg {
		ctx, cancel := context.WithCancelCause(ctx)
		ctx, cancelTimeout := context.WithTimeoutCause(ctx, rexTimeout,
			fmt.Errorf("editor split: %v timeout: %w", rexTimeout, context.DeadlineExceeded))
		defer cancelTimeout()
		defer cancel(fmt.Errorf("editor split: done: %w", context.Canceled))
		err := c.Split(ctx, root, "sh", "-c", ed.Script)
		return splitMsg{path: path, script: ed.Script, err: err}
	}
}

// onSplit tells that the editor opens in a new pane. If Rex cannot open
// the pane, the editor runs in this pane.
func (m *model) onSplit(msg splitMsg) tea.Cmd {
	if msg.err == nil {
		m.flash("opened " + msg.path)
		return nil
	}
	m.log.Debug("editor split failed", "err", msg.err)
	return m.runHere(msg.path, msg.script)
}

// runHere runs the editor script in this pane, in place of rexdiff.
func (m *model) runHere(path, script string) tea.Cmd {
	return tea.ExecProcess(command(m.env, "sh", "-c", script), func(err error) tea.Msg {
		return editorDoneMsg{path: path, err: err}
	})
}

// runWindowEditor starts a window editor for path. The editor gets its
// own session and no terminal, so that it cannot read the keys of rexdiff.
// If the editor fails, the returned command gives the error and the last
// line of the stderr of the editor.
func (m *model) runWindowEditor(path, script string) tea.Cmd {
	wait, err := detach(m.env, "editor", "sh", "-c", script)
	if err != nil {
		m.flash("editor failed: " + err.Error())
		return nil
	}
	m.flash("opened " + path)
	return wait
}

// detach starts a program with the environment env in its own session.
// The program has no terminal, so it cannot read the keys of rexdiff. The
// returned command waits for the program. If the program fails, the
// command gives a detachedMsg with the last line of the stderr of the
// program.
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

// onEditorDone shows how the editor in this pane stops. An editor that
// stops with an error code can still save the file.
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

// openURL opens url with the URL opener of the settings, else with
// the opener of the system. As in herdr-reviewr, it opens only an http or
// an https URL. The opener runs with the environment env, and with no
// shell. A missing opener gives the error of [proc.NotInstalled]. The
// command waits for the opener.
func openURL(env []string, cfg config, url string) (tea.Cmd, error) {
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

// isWebURL reports whether url is http or https with text after the
// scheme, as herdr-reviewr checks it. A control character or a bidi
// mark can hide a part of the URL, so url must have none.
func isWebURL(url string) bool {
	lower := strings.ToLower(url)
	web := len(url) > len("http://") && strings.HasPrefix(lower, "http://") ||
		len(url) > len("https://") && strings.HasPrefix(lower, "https://")
	return web && !strings.ContainsFunc(url, func(r rune) bool {
		return unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r)
	})
}

// openerArgs returns the program and the arguments of the URL opener
// command, as herdr-reviewr splits it. The words split at spaces, and a
// quoted text is one word. Each {url} becomes url. Without {url}, url is
// the last argument. The check of the settings makes sure that the first
// word is a program.
func openerArgs(command, url string) []string {
	var args []string
	for _, w := range splitWords(command) {
		args = append(args, strings.ReplaceAll(w, "{url}", url))
	}
	if !strings.Contains(command, "{url}") {
		args = append(args, url)
	}
	return args
}

// splitWords splits command into words at spaces, as herdr-reviewr does.
// Single or double quotes keep the spaces in a word. The quotes do not
// stay in the word.
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

// command returns the command for an editor or a URL opener, with the
// environment env. The context of the command has no end, because the
// program must not stop when rexdiff stops. For example, a kill of the
// editor can lose the edits of the user.
func command(env []string, name string, args ...string) *exec.Cmd {
	return proc.Command(context.Background(), env, name, args...)
}
