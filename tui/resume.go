package tui

import (
	"encoding/json/v2"
	"os"
	"slices"
	"time"

	"github.com/rednafi/rexdiff/diff"
	"github.com/rednafi/rexdiff/internal/proc"
)

// After an update, rexdiff restarts in the same terminal. The old process
// saves the comments and the user's position to a file, and the new
// process reads that file and carries on from the same position.

// resumeState holds what a restart must keep: the comments, the scope, the
// view settings, the expanded folds and the position in each pane.
type resumeState struct {
	Tab   tab         `json:"tab"`
	Scope Scope       `json:"scope"`
	Base  string      `json:"base"`
	From  string      `json:"from"` // the oldest commit in the commits scope
	To    string      `json:"to"`   // the newest commit in the commits scope
	Turn  *resumeTurn `json:"turn"`

	// Files are stored by path, because the file lists can change during
	// a restart.
	ChangePath string     `json:"change_path"`
	FilePath   string     `json:"file_path"` // the file shown in the Files tab
	Changes    resumePane `json:"changes"`
	Viewer     resumePane `json:"viewer"` // the pane of the Files tab
	PR         resumePane `json:"pr"`

	Split    bool     `json:"split"`
	Wrap     bool     `json:"wrap"`
	Rendered bool     `json:"rendered"`
	ListPos  position `json:"list_pos"`
	// ListLeft is the file list position from older versions, which leave
	// ListPos "".
	ListLeft   bool `json:"list_left,omitzero"`
	ListHidden bool `json:"list_hidden"`
	FocusList  bool `json:"focus_list"`
	ListW      int  `json:"list_w"`
	ListPct    int  `json:"list_pct,omitzero"`

	Folds map[string]*expandedFolds `json:"folds"`

	Comments *diff.Comments `json:"comments"`
	LastSent string         `json:"last_sent"`
	Beside   string         `json:"beside"`
}

// resumeTurn is a [turnState] with exported fields for JSON. State from an
// older version may contain a "block" member, which Unmarshal ignores.
type resumeTurn struct {
	App     string    `json:"app"`
	Tree    string    `json:"tree"`
	Running bool      `json:"running"`
	At      time.Time `json:"at"`
}

// resumePane is the cursor and scroll position in a pane.
type resumePane struct {
	Cur  int `json:"cur"`
	Top  int `json:"top"`
	Xoff int `json:"xoff"` // the horizontal scroll
}

// place returns the position of p, for the resume state.
func (p *pane) place() resumePane { return resumePane{Cur: p.cur, Top: p.top, Xoff: p.scrollX} }

// restore moves p to the position s, clamped to its lines.
func (p *pane) restore(s resumePane) {
	p.cur, p.top, p.scrollX = min(s.Cur, p.last()), min(s.Top, p.last()), s.Xoff
}

// saveResume writes the resume state to a new temporary file and returns
// its path.
func (m *model) saveResume() (string, error) {
	s := resumeState{
		Tab:        m.tab,
		Scope:      m.scope,
		Base:       m.base,
		From:       m.oldest,
		To:         m.newest,
		Changes:    m.changes.pane.place(),
		Viewer:     m.allFiles.pane.place(),
		PR:         m.pr.pane.place(),
		Split:      m.split,
		Wrap:       m.wrap,
		Rendered:   m.rendered,
		ListPos:    m.navigatorPosition,
		ListHidden: m.navigatorHidden,
		FocusList:  m.navigatorFocused,
		ListW:      m.navigatorWidthSet,
		ListPct:    m.navigatorPct,
		Folds:      m.expandedFolds,
		Comments:   &m.comments,
		LastSent:   m.lastSent,
		Beside:     m.beside,
	}
	if t := m.turn; t != nil {
		s.Turn = &resumeTurn{App: t.app, Tree: t.tree, Running: t.running, At: t.at}
	}
	if m.pr.place != nil { // the PR has not loaded since the previous restart
		s.PR = *m.pr.place
	}
	if f := m.curChangedFile(); f != nil {
		s.ChangePath = f.Path
	}
	if m.allFiles.cur < len(m.allFiles.entries) {
		s.FilePath = m.allFiles.entries[m.allFiles.cur].Path
	}
	// Keep the places that the previous process saved if they have not
	// been restored yet.
	if r := m.resumeChanges; r != nil {
		s.ChangePath, s.Changes = r.ChangePath, r.Changes
	}
	if r := m.resumeFiles; r != nil {
		s.FilePath, s.Viewer = r.FilePath, r.Viewer
	}
	b, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	f, err := os.CreateTemp(proc.Getenv(m.env, "TMPDIR"), "rexdiff-state-*.json")
	if err != nil {
		return "", err
	}
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return "", err
	}
	return f.Name(), f.Close()
}

// loadResume reads the file written by saveResume and then removes it.
func loadResume(path string) (*resumeState, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	// If the removal fails, the file left in the temporary directory does
	// no harm.
	_ = os.Remove(path)
	s := &resumeState{Comments: new(diff.Comments)}
	if err := json.Unmarshal(b, s); err != nil {
		return nil, err
	}
	return s, nil
}

// restoreSettings restores the scope, view settings and comments from s,
// and keeps s to restore the places once the changes and the files have
// loaded. Call it before the first reload, which reads the files in the
// scope.
func (m *model) restoreSettings(s *resumeState) {
	m.resumeChanges, m.resumeFiles = s, s
	// Expand the directories above the current file before reading the
	// Files tab entries, so that a file inside an ignored directory
	// appears in the file list.
	m.allFiles.navigator.reveal(s.FilePath)
	m.allFiles.listing.want = true
	m.pr.place = &s.PR // the PR loads later, and onPR restores the position
	m.tab, m.scope, m.base, m.oldest, m.newest = s.Tab, s.Scope, s.Base, s.From, s.To
	if t := s.Turn; t != nil {
		m.turn = &turnState{app: t.App, tree: t.Tree, running: t.Running, at: t.At}
	}
	m.split, m.wrap, m.rendered = s.Split, s.Wrap, s.Rendered
	m.navigatorPosition, m.navigatorHidden = s.ListPos, s.ListHidden
	m.navigatorFocused, m.navigatorWidthSet = s.FocusList, s.ListW
	switch {
	case m.navigatorPosition != "":
		// The saved position takes precedence over the former ListLeft.
	case s.ListLeft:
		m.navigatorPosition = positionLeft
	default:
		m.navigatorPosition = positionRight
	}
	if s.ListPct > 0 {
		m.navigatorPct = s.ListPct
	}
	m.expandedFolds = s.Folds
	if s.Comments != nil {
		m.comments = *s.Comments
		m.comments.SetRev(m.diffRev())
	}
	m.lastSent, m.beside = s.LastSent, s.Beside
}

// restoreChanges selects the file saved in the resume state and moves the
// cursor of the Changes tab to the saved position, once the first reload
// has loaded the changes. The first file list restores the Files tab in
// [model.onFiles].
func (m *model) restoreChanges() {
	s := m.resumeChanges
	if s == nil || !m.changesLoaded {
		return
	}
	m.resumeChanges = nil
	m.changes.navigator.reveal(s.ChangePath)
	if i := slices.IndexFunc(m.changes.files, func(f changedFile) bool { return f.Path == s.ChangePath }); i >= 0 {
		m.changes.cur = i
	}
	m.changes.navigator.cur = -1 // put the list cursor on the current file
	m.buildBothNavigators()
	m.renderChanges()
	m.changes.pane.restore(s.Changes)
	m.rerender()
}
