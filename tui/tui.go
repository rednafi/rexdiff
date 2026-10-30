// Package tui implements the terminal UI of rexdiff.
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

// appName is also the label that rexdiff gives its own Rex block.
const appName = "rexdiff"

// Options configure Run.
type Options struct {
	Repo *vcs.Repo   // not nil
	Rex  *rex.Client // nil outside Rex
	// Config holds the settings as JSON from rexdiff.lua. Empty means
	// the defaults.
	Config string
	// Layout and Scope override the settings if not empty.
	Layout Layout
	Scope  Scope
	Agent  string // the block ID or label of the agent to select first
	Wrap   bool
	Base   string       // empty means the default branch
	Log    *slog.Logger // nil disables logging
	Notice string       // shows in the footer until rexdiff quits
	// Installer is nil if rexdiff cannot update itself.
	Installer *update.Installer
	// Resume is the state file that Run returned before an update. Run
	// removes it after it reads it.
	Resume string
	// Env replaces the process environment for the editor, URL opener,
	// clipboard tool and terminal detection.
	Env []string
}

func (o Options) validate() error {
	if o.Repo == nil {
		return errors.New("no repository")
	}
	return ValidateFlags(o.Layout, o.Scope)
}

// ValidateFlags checks the -layout and -scope values. An empty value is
// valid.
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

// ValidateConfig checks text, the settings as a JSON object.
func ValidateConfig(text string) error {
	_, err := parseConfig(text)
	return err
}

// Run runs the UI until the user quits or ctx is done. After an update,
// it returns the state file for [Options.Resume]. It then leaves the
// alternate screen on and the cursor hidden. If the restart fails, the
// caller must restore both.
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

var errQuit = fmt.Errorf("rexdiff quits: %w", context.Canceled)

// startModel only requests the load, so that the first frame does not
// wait for git.
func startModel(ctx context.Context, o Options, cfg config) model {
	m := newModel(ctx, o, cfg)
	var saved *resumeState
	if o.Resume != "" {
		var err error
		if saved, err = loadResume(o.Resume); err != nil {
			m.flash("restore of the comments failed: " + err.Error())
		}
	}
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
