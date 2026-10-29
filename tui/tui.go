// Package tui implements the terminal UI of rexdiff. It shows the changes
// in a repository, lets the user add review comments to lines, and sends
// the comments to a coding agent in Rex.
package tui

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	tea "charm.land/bubbletea/v2"

	"github.com/rednafi/rexdiff/internal/xctx"
	"github.com/rednafi/rexdiff/rex"
	"github.com/rednafi/rexdiff/update"
	"github.com/rednafi/rexdiff/vcs"
)

// appName is the name that rexdiff reports to Rex, and the label it gives
// its own block.
const appName = "rexdiff"

// Options configure Run.
type Options struct {
	Repo *vcs.Repo // the repository to review, not nil
	// Rex is the Rex session that the process runs in, or nil outside
	// Rex. Without Rex, the user can copy the review but not send it.
	Rex *rex.Client
	// Config holds the settings as JSON, read from rexdiff.lua. Empty
	// means the default settings.
	Config string
	// Layout and Scope, if not empty, override the settings. They come
	// from the -layout and -scope flags, and the error for an invalid
	// value names the flag.
	Layout Layout
	Scope  Scope
	// Agent is the block ID or label of the agent beside rexdiff, or "".
	// The agent picker selects it first.
	Agent string
	// Wrap wraps long lines instead of cutting them off. The w key turns
	// it on and off.
	Wrap bool
	// Base is the base of the branch scope. Empty means the repository's
	// default branch.
	Base string
	Log  *slog.Logger // nil disables logging
	// Notice, if not empty, describes a problem that the footer shows
	// until rexdiff quits.
	Notice string
	// Installer installs a newer release, or is nil if rexdiff cannot
	// update itself. If it is not nil, rexdiff checks for the latest
	// release.
	Installer *update.Installer
	// Resume is the state file that Run returned before an update, or "".
	// Run reads the file and then removes it.
	Resume string
	// Env is the environment for the editor, the URL opener and the
	// clipboard tool, such as [os.Environ]. Run looks these tools up in
	// its PATH, and reads $VISUAL, $EDITOR, $TMPDIR and the terminal
	// variables from it.
	Env []string
}

// validate returns an error if Run cannot use o.
func (o Options) validate() error {
	if o.Repo == nil {
		return errors.New("no repository")
	}
	return ValidateFlags(o.Layout, o.Scope)
}

// ValidateFlags returns an error if the values of the -layout or -scope
// flags are invalid. An empty value means the default and is valid.
func ValidateFlags(layout Layout, scope Scope) error {
	if layout != "" {
		if err := validateChoice("-layout", layout, layouts); err != nil {
			return err
		}
	}
	if scope != "" {
		return validateChoice("-scope", scope, configScopes)
	}
	return nil
}

// ValidateConfig returns an error if text, the settings as a JSON object,
// is invalid.
func ValidateConfig(text string) error {
	_, err := parseConfig(text)
	return err
}

// Run runs the UI in the terminal until the user quits or ctx is done. It
// fails at once if the settings are invalid, Repo is nil, or Layout or
// Scope is unknown. After an update, it returns the saved state file to
// pass as [Options.Resume], and leaves the alternate screen on with the
// cursor hidden; if the restart fails, the caller must restore both.
func Run(ctx context.Context, o Options) (resume string, err error) {
	if err := o.validate(); err != nil {
		return "", err
	}
	cfg, err := parseConfig(o.Config)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(errQuit)

	m := startModel(ctx, o, cfg)
	// Bubble Tea reads TERM and COLORTERM from env. Given a nil env, it
	// would read the process environment instead.
	env := o.Env
	if env == nil {
		env = []string{}
	}
	program := tea.NewProgram(&m, tea.WithContext(ctx), tea.WithEnvironment(env))
	var wg sync.WaitGroup
	if o.Rex != nil {
		// Events returns only when ctx is done, and then returns its
		// cause, so the error can be ignored.
		wg.Go(func() {
			_ = o.Rex.Events(ctx, func(e rex.Event) { program.Send(eventMsg(e)) })
		})
	}
	final, err := program.Run()
	cancel(errQuit)
	wg.Wait()
	if fm, ok := final.(*model); ok && err == nil {
		if fm.resumePath != "" {
			// Keep the last frame on screen until the new rexdiff draws,
			// so that the shell does not appear during the restart.
			fmt.Print("\x1b[?1049h\x1b[?25l\x1b[H" + fm.View().Content)
		}
		return fm.resumePath, nil
	}
	return "", err
}

// errQuit is the cancellation cause for Run's background work when
// rexdiff quits.
var errQuit = fmt.Errorf("rexdiff quits: %w", context.Canceled)

// startModel returns the model that Run displays. It restores the
// settings saved in o.Resume, asks for the changes to load, and labels the
// rexdiff block in Rex. [model.Init] starts the loading, so that the first
// frame does not wait for git.
func startModel(ctx context.Context, o Options, cfg config) model {
	m := newModel(ctx, o, cfg)
	var saved *resumeState
	if o.Resume != "" {
		var err error
		if saved, err = loadResume(o.Resume); err != nil {
			m.flash("restore of the comments failed: " + err.Error())
		}
	}
	// loadResume returns a nil state with an error.
	if saved != nil {
		m.restoreSettings(saved)
	}
	m.requestReload()
	m.changes.pane.note = "loading changes…"
	if o.Rex != nil && o.Rex.Block() != "" {
		ctx, cancel := xctx.WithTimeout(ctx, rexTimeout, "rex label")
		if err := o.Rex.SetLabel(ctx, o.Rex.Block(), appName); err != nil {
			m.log.Debug("set label failed", "err", err)
		}
		cancel()
	}
	return m
}
