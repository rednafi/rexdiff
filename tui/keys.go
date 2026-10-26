package tui

import (
	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/lipgloss/v2"
)

// keyMap uses the action names and the default keys of herdr-reviewr. The
// keybindings setting uses the same names. The settings do not change the
// keys of the comment box and of the find.
type keyMap struct {
	// Move.
	Down, Up, PageDown, PageUp, HalfDown, HalfUp, Top, Bottom  key.Binding
	NextHunk, PrevHunk, NextFile, PrevFile, Focus, Left, Right key.Binding

	// Scopes and tabs.
	ScopeUncommitted, ScopeBranch, ScopeTurn, ScopeCommits key.Binding
	BasePick, CommitPick, TabChanges, TabFiles, TabPR      key.Binding

	// Comments.
	Select, Comment, Edit, Delete, NextComment, PrevComment key.Binding
	Comments, Send, Copy                                    key.Binding

	// View and other actions.
	Search, Find, GotoLine, Rendered, Split, Wrap key.Binding
	NavPosition, NavHide, NavGrow, NavShrink      key.Binding
	OpenPR, Refresh, Keys, Quit, QuitDiscard      key.Binding
	// Upgrade installs a newer release. It works only while the footer
	// tells about a newer release.
	Upgrade key.Binding

	// The comment box. Cancel also clears the selection.
	Save, Newline, Cancel key.Binding

	// FindNext and FindPrev move to the next and the previous hit of a find.
	FindNext, FindPrev key.Binding
}

// bind returns a binding of keys, with helpKey and desc in the help.
func bind(keys []string, helpKey, desc string) key.Binding {
	return key.NewBinding(key.WithKeys(keys...), key.WithHelp(helpKey, desc))
}

// hint returns a copy of b with other help text. The copy keeps the keys
// of b, because the help does not show a binding that has no keys.
func hint(b key.Binding, helpKey, desc string) key.Binding {
	b.SetHelp(helpKey, desc)
	return b
}

func defaultKeys() keyMap {
	return keyMap{
		Down:     bind([]string{"j", "down"}, "j/k", "move"),
		Up:       bind([]string{"k", "up"}, "k", "up"),
		PageDown: bind([]string{"pgdown"}, "pgdn", "page down"),
		PageUp:   bind([]string{"pgup"}, "pgup", "page up"),
		HalfDown: bind([]string{"ctrl+d"}, "ctrl+d/u", "half page"),
		HalfUp:   bind([]string{"ctrl+u"}, "ctrl+u", "half page up"),
		Top:      bind([]string{"home"}, "home", "top"),
		Bottom:   bind([]string{"end"}, "end", "bottom"),
		NextHunk: bind([]string{"]"}, "]/[", "next/prev change"),
		PrevHunk: bind([]string{"["}, "[", "prev change"),
		NextFile: bind([]string{"f"}, "f/F", "next/prev file"),
		PrevFile: bind([]string{"F"}, "F", "prev file"),
		Focus:    bind([]string{"tab"}, "tab", "switch pane"),
		Left:     bind([]string{"left"}, "←/→", "scroll sideways"),
		Right:    bind([]string{"right"}, "→", "scroll right"),

		ScopeUncommitted: bind([]string{"u"}, "u", "uncommitted"),
		ScopeBranch:      bind([]string{"b"}, "b", "branch"),
		ScopeTurn:        bind([]string{"t"}, "t", "last turn"),
		ScopeCommits:     bind([]string{"g"}, "g", "commits"),
		BasePick:         bind([]string{"B"}, "B", "pick base branch"),
		CommitPick:       bind([]string{"G"}, "G", "pick commits"),
		TabChanges:       bind([]string{"1"}, "1/2/3", "changes/files/PR"),
		TabFiles:         bind([]string{"2"}, "2", "all files"),
		TabPR:            bind([]string{"3"}, "3", "PR"),

		Select:      bind([]string{"v"}, "v", "select lines"),
		Comment:     bind([]string{"c"}, "c", "comment"),
		Edit:        bind([]string{"e"}, "e", "edit comment / open editor"),
		Delete:      bind([]string{"d"}, "d", "delete comment"),
		NextComment: bind([]string{"n"}, "n/N", "next/prev comment"),
		PrevComment: bind([]string{"N"}, "N", "prev comment"),
		Comments:    bind([]string{"l"}, "l", "list comments"),
		Send:        bind([]string{"s", "S"}, "s", "send to agent"),
		Copy:        bind([]string{"y", "Y"}, "y", "copy comments"),

		Search:      bind([]string{"/"}, "/", "search files & code"),
		Find:        bind([]string{"ctrl+f"}, "ctrl+f", "find in file"),
		GotoLine:    bind([]string{":"}, ":", "go to line"),
		Rendered:    bind([]string{"m"}, "m", "rendered markdown"),
		Split:       bind([]string{"|"}, "|", "side by side"),
		Wrap:        bind([]string{"w"}, "w", "wrap lines"),
		NavPosition: bind([]string{"p"}, "p", "move file list"),
		NavHide:     bind([]string{"z"}, "z", "hide file list"),
		NavGrow:     bind([]string{"<"}, "</>", "widen/narrow list"),
		NavShrink:   bind([]string{">"}, ">", "narrow list"),
		OpenPR:      bind([]string{"o"}, "o", "open PR in browser"),
		Refresh:     bind([]string{"r"}, "r", "refresh"),
		Upgrade:     bind([]string{"U"}, "U", "update rexdiff"),
		Keys:        bind([]string{"?"}, "?", "all keys"),
		Quit:        bind([]string{"q", "ctrl+c"}, "q", "quit"),
		QuitDiscard: bind([]string{"Q"}, "Q", "quit, drop comments"),

		Save: bind([]string{"enter"}, "enter", "save"),
		Newline: bind(
			[]string{"shift+enter", "alt+enter", "ctrl+j"},
			"shift+enter",
			"new line",
		),
		Cancel: bind([]string{"esc"}, "esc", "cancel"),

		FindNext: key.NewBinding(key.WithKeys("n")),
		FindPrev: key.NewBinding(key.WithKeys("N")),
	}
}

type overlayKeyMap struct {
	Choose, Close, Up, Down key.Binding

	// ListDown, ListUp, and Delete work in the lists that have no input.
	ListDown, ListUp, Delete key.Binding

	// Mark starts or clears a range in the commit picker.
	Mark key.Binding

	// Quit and Stay are the answers to the prompt before a quit.
	Quit, Stay key.Binding
}

// overlayKeys is the keys of the overlays. The settings do not change
// them.
var overlayKeys = overlayKeyMap{
	Choose:   key.NewBinding(key.WithKeys("enter")),
	Close:    key.NewBinding(key.WithKeys("esc")),
	Up:       key.NewBinding(key.WithKeys("up", "ctrl+p", "ctrl+k")),
	Down:     key.NewBinding(key.WithKeys("down", "ctrl+n", "ctrl+j", "tab")),
	ListDown: key.NewBinding(key.WithKeys("j")),
	ListUp:   key.NewBinding(key.WithKeys("k")),
	Delete:   key.NewBinding(key.WithKeys("d")),
	Mark:     key.NewBinding(key.WithKeys("space")),
	Quit:     key.NewBinding(key.WithKeys("q", "y", "enter", "Q")),
	Stay:     key.NewBinding(key.WithKeys("esc", "n")),
}

// binding is an action of the keybindings setting and the key binding that
// it changes.
type binding struct {
	name string // the name in the keybindings setting
	keys []string
	key  *key.Binding
}

// bindings returns each action of c with its key binding in k.
func (k *keyMap) bindings(c keybindings) []binding {
	return []binding{
		{"down", c.Down, &k.Down},
		{"up", c.Up, &k.Up},
		{"page-down", c.PageDown, &k.PageDown},
		{"page-up", c.PageUp, &k.PageUp},
		{"half-down", c.HalfDown, &k.HalfDown},
		{"half-up", c.HalfUp, &k.HalfUp},
		{"collapse", c.Collapse, &k.Left},
		{"expand", c.Expand, &k.Right},
		{"next-hunk", c.NextHunk, &k.NextHunk},
		{"prev-hunk", c.PrevHunk, &k.PrevHunk},
		{"next-file", c.NextFile, &k.NextFile},
		{"prev-file", c.PrevFile, &k.PrevFile},
		{"focus", c.Focus, &k.Focus},
		{"scope-uncommitted", c.ScopeUncommitted, &k.ScopeUncommitted},
		{"scope-branch", c.ScopeBranch, &k.ScopeBranch},
		{"scope-last-turn", c.ScopeLastTurn, &k.ScopeTurn},
		{"scope-commits", c.ScopeCommits, &k.ScopeCommits},
		{"base-pick", c.BasePick, &k.BasePick},
		{"commit-pick", c.CommitPick, &k.CommitPick},
		{"tab-changes", c.TabChanges, &k.TabChanges},
		{"tab-all-files", c.TabAllFiles, &k.TabFiles},
		{"tab-pr", c.TabPR, &k.TabPR},
		{"select", c.Select, &k.Select},
		{"comment", c.Comment, &k.Comment},
		{"edit", c.Edit, &k.Edit},
		{"delete", c.Delete, &k.Delete},
		{"next-comment", c.NextComment, &k.NextComment},
		{"prev-comment", c.PrevComment, &k.PrevComment},
		{"comments", c.Comments, &k.Comments},
		{"send", c.Send, &k.Send},
		{"copy", c.Copy, &k.Copy},
		{"search", c.Search, &k.Search},
		{"find", c.Find, &k.Find},
		{"goto-line", c.GotoLine, &k.GotoLine},
		{"rendered", c.Rendered, &k.Rendered},
		{"split", c.Split, &k.Split},
		{"wrap", c.Wrap, &k.Wrap},
		{"navigator-position", c.NavigatorPosition, &k.NavPosition},
		{"navigator-hide", c.NavigatorHide, &k.NavHide},
		{"navigator-grow", c.NavigatorGrow, &k.NavGrow},
		{"navigator-shrink", c.NavigatorShrink, &k.NavShrink},
		{"open-pr", c.OpenPR, &k.OpenPR},
		{"refresh", c.Refresh, &k.Refresh},
		{"keys", c.Keys, &k.Keys},
		{"quit", c.Quit, &k.Quit},
		{"quit-discard", c.QuitDiscard, &k.QuitDiscard},
	}
}

// applyConfig sets the keys of each action that c sets.
func (k *keyMap) applyConfig(c keybindings) {
	for _, b := range k.bindings(c) {
		if b.keys == nil {
			continue
		}
		b.key.SetKeys(b.keys...)
		if len(b.keys) > 0 {
			b.key.SetHelp(b.keys[0], b.key.Help().Desc)
		}
	}
}

// helpKeys implements help.KeyMap for the mode and the tab of the program.
type helpKeys struct {
	k       keyMap
	mode    mode
	tab     tab
	split   bool
	visual  bool
	finding bool
	onFold  bool // the cursor is on a fold
}

// ShortHelp returns the keys of the footer.
func (h helpKeys) ShortHelp() []key.Binding {
	k := h.k
	if h.split {
		k.Split.SetHelp(k.Split.Help().Key, "top to bottom")
	}
	switch {
	case h.mode == modeComment:
		return []key.Binding{k.Save, k.Newline, k.Cancel}
	case h.finding:
		return []key.Binding{
			hint(k.FindNext, "n/N", "next/prev match"),
			hint(k.Cancel, "esc", "stop finding"),
		}
	case h.visual:
		return []key.Binding{
			k.Comment,
			hint(k.Down, "j/k", "extend"),
			hint(k.Cancel, "esc", "clear"),
		}
	case h.tab == tabPR:
		return []key.Binding{k.Down, k.OpenPR, k.Refresh, k.TabChanges, k.Keys, k.Quit}
	case h.onFold:
		return []key.Binding{
			hint(k.Right, k.Right.Help().Key, "show the lines"), k.Comment, k.Send, k.NextHunk, k.Keys, k.Quit,
		}
	}
	return []key.Binding{k.Comment, k.Send, k.NextHunk, k.NextFile, k.Split, k.Keys, k.Quit}
}

// FullHelp returns the columns of keys that ? shows.
func (h helpKeys) FullHelp() [][]key.Binding {
	k := h.k
	move := []key.Binding{
		k.Down, k.HalfDown, k.NextHunk, k.NextFile, k.Focus, k.Left, k.GotoLine,
	}
	scopes := []key.Binding{
		k.TabChanges, k.ScopeUncommitted, k.ScopeBranch, k.ScopeTurn, k.ScopeCommits,
		k.BasePick, k.CommitPick,
	}
	comments := []key.Binding{
		k.Select, k.Comment, k.Edit, k.Delete, k.NextComment, k.Comments, k.Send, k.Copy,
	}
	view := []key.Binding{
		k.Search, k.Find, k.Rendered, k.Split, k.Wrap, k.NavPosition, k.NavHide, k.NavGrow,
	}
	other := []key.Binding{k.OpenPR, k.Refresh, k.Keys, k.Quit, k.QuitDiscard}
	return [][]key.Binding{move, scopes, comments, view, other}
}

// newHelp styles the footer with the terminal palette.
func newHelp() help.Model {
	h := help.New()
	keyS := lipgloss.NewStyle().Bold(true)
	descS := lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	h.Styles.ShortKey, h.Styles.FullKey = keyS, keyS
	h.Styles.ShortDesc, h.Styles.FullDesc = descS, descS
	h.Styles.ShortSeparator, h.Styles.FullSeparator = descS, descS
	h.Styles.Ellipsis = descS
	h.ShortSeparator, h.FullSeparator = "    ", "    "
	return h
}
