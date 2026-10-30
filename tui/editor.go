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

func (m *model) onEditorPane(msg editorPaneMsg) tea.Cmd {
	if msg.err == nil {
		m.flash("opened " + msg.path)
		return nil
	}
	m.log.Debug("editor split failed", "err", msg.err)
	return m.runEditorHere(msg.path, msg.script)
}

func (m *model) runEditorHere(path, script string) tea.Cmd {
	return tea.ExecProcess(command(m.env, "sh", "-c", script), func(err error) tea.Msg {
		return editorDoneMsg{path: path, err: err}
	})
}

func (m *model) runEditorWindow(path, script string) tea.Cmd {
	wait, err := detach(m.env, "editor", "sh", "-c", script)
	if err != nil {
		m.flash("editor failed: " + err.Error())
		return nil
	}
	m.flash("opened " + path)
	return wait
}

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

// onEditorDone always renders again. An editor that exits with an error
// can still save the file.
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

// urlOpenerCmd runs the opener without a shell.
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

// isWebURL rejects control characters and bidi marks, because these can
// hide part of the URL.
func isWebURL(url string) bool {
	lower := strings.ToLower(url)
	web := len(url) > len("http://") && strings.HasPrefix(lower, "http://") ||
		len(url) > len("https://") && strings.HasPrefix(lower, "https://")
	return web && !strings.ContainsFunc(url, func(r rune) bool {
		return unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r)
	})
}

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

// command uses [context.Background] so that the program keeps running
// when rexdiff exits. Killing an editor can lose the user's edits.
func command(env []string, name string, args ...string) *exec.Cmd {
	return proc.Command(context.Background(), env, name, args...)
}
