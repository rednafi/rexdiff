// Package tui is the terminal program of rexdiff. It shows the changes in a
// repository and takes review comments on the lines. It sends the comments
// to a coding agent in Rex.
package tui

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	tea "charm.land/bubbletea/v2"

	"github.com/rednafi/rexdiff/rex"
	"github.com/rednafi/rexdiff/vcs"
)

// appName is the name that rexdiff reports to Rex, and the label of its
// block.
const appName = "rexdiff"

// Options configure Run.
type Options struct {
	Repo *vcs.Repo // the repository to review, not nil
	// Rex is the Rex session of the process, or nil outside Rex. Without
	// Rex, the user cannot send the review. The user can only copy it.
	Rex *rex.Client
	// Config is the settings as JSON, from rexdiff.lua. An empty text
	// means the default settings.
	Config string
	// Layout and Scope override the settings if they are not empty. Set
	// them from the command-line flags -layout and -scope. The error of a
	// bad value names the flag.
	Layout Layout
	Scope  Scope
	// Agent is the block ID or the label of the agent that the program
	// opens beside, or "". The agent picker selects it first.
	Agent string
	// Wrap continues long lines on the next lines, instead of cutting
	// them. The key w changes it.
	Wrap bool
	// Base is the base of the branch scope. An empty value means the
	// default branch of the repository.
	Base string
	Log  *slog.Logger // nil means no log
	// Notice, if not empty, is a problem that the footer shows until the
	// program quits.
	Notice string
	// Updater installs a newer release, or is nil if rexdiff cannot update
	// itself. If it is not nil, the program asks for the latest release.
	Updater Updater
	// Resume is the file of the state that Run returned before an update,
	// or "". Run reads it and removes it.
	Resume string
	// Env is the environment of the editor, the URL opener, and the
	// clipboard tool, for example the environment of the process from
	// os.Environ. Run finds them in its PATH, and reads $VISUAL and
	// $EDITOR from it.
	Env []string
}

// check returns an error for options that Run cannot use.
func (o Options) check() error {
	if o.Repo == nil {
		return errors.New("no repository")
	}
	return CheckFlags(o.Layout, o.Scope)
}

// CheckFlags returns the error of the values of the flags -layout and
// -scope, or nil if rexdiff accepts them. An empty value is the default.
func CheckFlags(layout Layout, scope Scope) error {
	if layout != "" {
		if err := choice("-layout", layout, layouts); err != nil {
			return err
		}
	}
	if scope != "" {
		return choice("-scope", scope, configScopes)
	}
	return nil
}

// CheckConfig returns the error of the settings text, a JSON object, or
// nil if rexdiff accepts it.
func CheckConfig(text string) error {
	_, err := parseConfig(text)
	return err
}

// Run shows the review program in the terminal. It returns when the user
// quits or when ctx is done. Run returns an error before it starts if the
// settings are not valid. It also returns an error if Repo is nil, or if
// Run does not know the Layout or the Scope. After an update, Run returns
// the file of the saved state. The caller then runs the new rexdiff with
// Options.Resume set to that file.
func Run(ctx context.Context, o Options) (resume string, err error) {
	if err := o.check(); err != nil {
		return "", err
	}
	cfg, err := parseConfig(o.Config)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(errQuit)

	m := startModel(ctx, o, cfg)
	program := tea.NewProgram(&m, tea.WithContext(ctx))
	var wg sync.WaitGroup
	if o.Rex != nil {
		// Watch returns only the cause of ctx, when ctx is done.
		wg.Go(func() {
			_ = o.Rex.Watch(ctx, func(e rex.Event) { program.Send(eventMsg(e)) })
		})
	}
	final, err := program.Run()
	cancel(errQuit)
	wg.Wait()
	if fm, ok := final.(*model); ok && err == nil {
		if fm.resumePath != "" {
			// Keep the last screen until the new rexdiff draws, so that
			// the restart does not show the shell of the pane.
			fmt.Print("\x1b[?1049h\x1b[?25l\x1b[H" + fm.View().Content)
		}
		return fm.resumePath, nil
	}
	return "", err
}

// errQuit is the cause of the stop of the work of Run when rexdiff quits.
var errQuit = fmt.Errorf("rexdiff quits: %w", context.Canceled)

// startModel returns the model that Run shows. It starts the watcher,
// reads the state of o.Resume, loads the changes, and labels the block of
// the program in Rex.
func startModel(ctx context.Context, o Options, cfg config) model {
	m := newModel(ctx, o, cfg)
	if err := m.startWatch(); err != nil {
		m.dropWatch(err)
	}
	var saved *resumeState
	if o.Resume != "" {
		var err error
		if saved, err = loadResume(o.Resume); err != nil {
			m.flash("restore of the comments failed: " + err.Error())
		} else {
			m.restoreSettings(saved)
		}
	}
	m.loadNow()
	if saved != nil {
		m.restorePlaces(saved)
	}
	if o.Rex != nil && o.Rex.Block() != "" {
		if err := o.Rex.SetLabel(ctx, o.Rex.Block(), appName); err != nil {
			m.log.Debug("set label failed", "err", err)
		}
	}
	return m
}
