package tui

import (
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"github.com/charmbracelet/x/ansi"
)

// keyMap uses the action names and the default keys of herdr-reviewr.
// The fields have the names of the actions in the keybindings setting.
// Focus and Update are actions of rexdiff only. The settings do not change
// the keys of the comment box and of the find.
type keyMap struct {
	// Move.
	Down, Up, PageDown, PageUp, HalfDown, HalfUp, Collapse, Expand key.Binding
	NextHunk, PrevHunk, NextFile, PrevFile, Focus                  key.Binding

	// Scopes and tabs.
	ScopeUncommitted, ScopeBranch, ScopeLastTurn, ScopeCommits key.Binding
	BasePick, CommitPick, TabChanges, TabAllFiles, TabPR       key.Binding

	// Comments.
	Select, Comment, Edit, Delete, NextComment, PrevComment key.Binding
	Comments, Send, Copy                                    key.Binding

	// View and other actions.
	Search, Find, GotoLine, Rendered, Split, Wrap                    key.Binding
	NavigatorPosition, NavigatorHide, NavigatorGrow, NavigatorShrink key.Binding
	OpenPR, Refresh, Keys, Quit, QuitDiscard                         key.Binding
	// Update installs a newer release. It works only while the footer
	// tells about a newer release.
	Update key.Binding

	// The comment box. Cancel also clears the selection.
	Save, Newline, Cancel key.Binding
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

// defaultKeys returns the keys of herdr-reviewr, with the help text of
// each action. Some actions share one help label, for example "j/k move".
// Then the label is on the first action of the pair.
func defaultKeys() keyMap {
	return keyMap{
		Down:     bind([]string{"j", "down"}, "j/k", "move"),
		Up:       bind([]string{"k", "up"}, "k", "up"),
		PageDown: bind([]string{"pgdown"}, "pgdn", "page down"),
		PageUp:   bind([]string{"pgup"}, "pgup", "page up"),
		HalfDown: bind([]string{"ctrl+d"}, "ctrl+d/u", "half page"),
		HalfUp:   bind([]string{"ctrl+u"}, "ctrl+u", "half page up"),
		NextHunk: bind([]string{"]"}, "]/[", "next/prev change"),
		PrevHunk: bind([]string{"["}, "[", "prev change"),
		NextFile: bind([]string{"f"}, "f/F", "next/prev file"),
		PrevFile: bind([]string{"F"}, "F", "prev file"),
		Focus:    bind([]string{"tab"}, "tab", "switch pane"),
		Collapse: bind([]string{"left"}, "←/→", "scroll sideways"),
		Expand:   bind([]string{"right"}, "→", "scroll right"),

		ScopeUncommitted: bind([]string{"u"}, "u", "uncommitted"),
		ScopeBranch:      bind([]string{"b"}, "b", "branch"),
		ScopeLastTurn:    bind([]string{"t"}, "t", "last turn"),
		ScopeCommits:     bind([]string{"g"}, "g", "commits"),
		BasePick:         bind([]string{"B"}, "B", "pick base branch"),
		CommitPick:       bind([]string{"G"}, "G", "pick commits"),
		TabChanges:       bind([]string{"1"}, "1/2/3", "changes/files/PR"),
		TabAllFiles:      bind([]string{"2"}, "2", "all files"),
		TabPR:            bind([]string{"3"}, "3", "PR"),

		Select:      bind([]string{"v"}, "v", "select lines"),
		Comment:     bind([]string{"c"}, "c", "comment"),
		Edit:        bind([]string{"e"}, "e", "edit comment/open editor"),
		Delete:      bind([]string{"d"}, "d", "delete comment"),
		NextComment: bind([]string{"n"}, "n/N", "next/prev comment"),
		PrevComment: bind([]string{"N"}, "N", "prev comment"),
		Comments:    bind([]string{"l"}, "l", "list comments"),
		Send:        bind([]string{"s", "S"}, "s", "send to agent"),
		Copy:        bind([]string{"y", "Y"}, "y", "copy comments"),

		Search:            bind([]string{"/"}, "/", "search files and code"),
		Find:              bind([]string{"ctrl+f"}, "ctrl+f", "find in file"),
		GotoLine:          bind([]string{":"}, ":", "go to line"),
		Rendered:          bind([]string{"m"}, "m", "rendered markdown"),
		Split:             bind([]string{"|"}, "|", "side by side"),
		Wrap:              bind([]string{"w"}, "w", "wrap lines"),
		NavigatorPosition: bind([]string{"p"}, "p", "move list"),
		NavigatorHide:     bind([]string{"z"}, "z", "hide list"),
		NavigatorGrow:     bind([]string{"<"}, "</>", "resize list"),
		NavigatorShrink:   bind([]string{">"}, ">", "narrow list"),
		OpenPR:            bind([]string{"o"}, "o", "open PR in browser"),
		Refresh:           bind([]string{"r"}, "r", "refresh"),
		Update:            bind([]string{"U"}, "U", "update rexdiff"),
		Keys:              bind([]string{"?"}, "?", "all keys"),
		Quit:              bind([]string{"q"}, "q", "quit"),
		QuitDiscard:       bind([]string{"Q"}, "Q", "quit, drop comments"),

		Save: bind([]string{"enter"}, "enter", "save"),
		Newline: bind(
			[]string{"shift+enter", "alt+enter", "ctrl+j"},
			"shift+enter",
			"new line",
		),
		Cancel: bind([]string{"esc"}, "esc", "cancel"),
	}
}

// overlayKeyMap has the keys of the overlays. The settings do not change
// them.
type overlayKeyMap struct {
	Choose, Close, Up, Down key.Binding

	// ListDown and ListUp work in the agent picker.
	ListDown, ListUp key.Binding
	// Flip switches the mode of the search. PreviewUp and PreviewDown
	// scroll the preview of the search.
	Flip, PreviewUp, PreviewDown key.Binding
}

// overlayKeys has the keys of the overlays. The settings do not change
// these keys.
var overlayKeys = overlayKeyMap{
	Choose: key.NewBinding(key.WithKeys("enter")),
	Close:  key.NewBinding(key.WithKeys("esc")),
	Up:     key.NewBinding(key.WithKeys("up", "ctrl+p", "ctrl+k")),
	Down:   key.NewBinding(key.WithKeys("down", "ctrl+n", "ctrl+j")),
	Flip:   key.NewBinding(key.WithKeys("tab")),
	// The keys of the search are the same as in herdr-reviewr.
	PreviewUp:   key.NewBinding(key.WithKeys("pgup")),
	PreviewDown: key.NewBinding(key.WithKeys("pgdown")),
	ListDown:    key.NewBinding(key.WithKeys("j")),
	ListUp:      key.NewBinding(key.WithKeys("k")),
}

// binding is an action of the keybindings setting and the key binding that
// it changes.
type binding struct {
	name string // the name in the keybindings setting
	keys []string
	key  *key.Binding
}

// bindings returns each action of c with its key binding in k, in the
// order of the table of herdr-reviewr. The check of the keybindings
// setting uses this order to name the actions of a key that two actions
// use. herdr-reviewr has no focus action. The update is last. Its key is
// fixed, and the check keeps it free.
func (k *keyMap) bindings(c keybindings) []binding {
	return []binding{
		{"down", c.Down, &k.Down},
		{"up", c.Up, &k.Up},
		{"next-hunk", c.NextHunk, &k.NextHunk},
		{"prev-hunk", c.PrevHunk, &k.PrevHunk},
		{"next-file", c.NextFile, &k.NextFile},
		{"prev-file", c.PrevFile, &k.PrevFile},
		{"collapse", c.Collapse, &k.Collapse},
		{"expand", c.Expand, &k.Expand},
		{"page-up", c.PageUp, &k.PageUp},
		{"page-down", c.PageDown, &k.PageDown},
		{"half-up", c.HalfUp, &k.HalfUp},
		{"half-down", c.HalfDown, &k.HalfDown},
		{"focus", c.Focus, &k.Focus},
		{"scope-uncommitted", c.ScopeUncommitted, &k.ScopeUncommitted},
		{"scope-branch", c.ScopeBranch, &k.ScopeBranch},
		{"scope-last-turn", c.ScopeLastTurn, &k.ScopeLastTurn},
		{"scope-commits", c.ScopeCommits, &k.ScopeCommits},
		{"base-pick", c.BasePick, &k.BasePick},
		{"commit-pick", c.CommitPick, &k.CommitPick},
		{"tab-changes", c.TabChanges, &k.TabChanges},
		{"tab-all-files", c.TabAllFiles, &k.TabAllFiles},
		{"tab-pr", c.TabPR, &k.TabPR},
		{"wrap", c.Wrap, &k.Wrap},
		{"rendered", c.Rendered, &k.Rendered},
		{"split", c.Split, &k.Split},
		{"navigator-position", c.NavigatorPosition, &k.NavigatorPosition},
		{"navigator-hide", c.NavigatorHide, &k.NavigatorHide},
		{"navigator-grow", c.NavigatorGrow, &k.NavigatorGrow},
		{"navigator-shrink", c.NavigatorShrink, &k.NavigatorShrink},
		{"select", c.Select, &k.Select},
		{"comment", c.Comment, &k.Comment},
		{"edit", c.Edit, &k.Edit},
		{"delete", c.Delete, &k.Delete},
		{"next-comment", c.NextComment, &k.NextComment},
		{"prev-comment", c.PrevComment, &k.PrevComment},
		{"comments", c.Comments, &k.Comments},
		{"search", c.Search, &k.Search},
		{"find", c.Find, &k.Find},
		{"goto-line", c.GotoLine, &k.GotoLine},
		{"keys", c.Keys, &k.Keys},
		{"send", c.Send, &k.Send},
		{"copy", c.Copy, &k.Copy},
		{"open-pr", c.OpenPR, &k.OpenPR},
		{"refresh", c.Refresh, &k.Refresh},
		{"quit", c.Quit, &k.Quit},
		{"quit-discard", c.QuitDiscard, &k.QuitDiscard},
		{"update", nil, &k.Update},
	}
}

// applyConfig sets the keys of each action that c sets. The check of the
// setting accepts each key, so keyName does not fail. The help shows the
// first key of a changed action, as herdr-reviewr spells it. A label of a
// pair, for example "j/k move", shows the first key of each action.
func (k *keyMap) applyConfig(c keybindings) {
	changed := map[*key.Binding]bool{}
	for _, b := range k.bindings(c) {
		if len(b.keys) == 0 {
			continue
		}
		keys := make([]string, len(b.keys))
		for i, s := range b.keys {
			keys[i], _ = keyName(s)
		}
		b.key.SetKeys(keys...)
		b.key.SetHelp(firstKey(*b.key), b.key.Help().Desc)
		changed[b.key] = true
	}
	for _, group := range k.pairs() {
		if !slices.ContainsFunc(group, func(b *key.Binding) bool { return changed[b] }) {
			continue
		}
		names := make([]string, len(group))
		for i, b := range group {
			names[i] = firstKey(*b)
		}
		group[0].SetHelp(strings.Join(names, "/"), group[0].Help().Desc)
	}
}

// pairs returns the groups of actions that share one help label, for
// example "j/k move". The label is on the first action of each group.
func (k *keyMap) pairs() [][]*key.Binding {
	return [][]*key.Binding{
		{&k.Down, &k.Up},
		{&k.HalfDown, &k.HalfUp},
		{&k.NextHunk, &k.PrevHunk},
		{&k.NextFile, &k.PrevFile},
		{&k.Collapse, &k.Expand},
		{&k.TabChanges, &k.TabAllFiles, &k.TabPR},
		{&k.NextComment, &k.PrevComment},
		{&k.NavigatorGrow, &k.NavigatorShrink},
	}
}

// namedKeys are the keys that are not one character. Each has its name as
// herdr-reviewr spells it, its bubbletea name, and its label on the
// screen.
var namedKeys = []namedKey{
	{"left", "left", "←"},
	{"right", "right", "→"},
	{"up", "up", "↑"},
	{"down", "down", "↓"},
	{"pageup", "pgup", "PageUp"},
	{"pagedown", "pgdown", "PageDown"},
}

// namedKey is a key that is not one character.
type namedKey struct{ name, tea, label string }

// teaKey returns the named key whose bubbletea name is name without its
// modifier, and the modifier. ok is false if no named key has this name.
func teaKey(name string) (mod string, k namedKey, ok bool) {
	mod, rest := cutModifier(name)
	i := slices.IndexFunc(namedKeys, func(k namedKey) bool { return k.tea == rest })
	if i < 0 {
		return mod, namedKey{}, false
	}
	return mod, namedKeys[i], true
}

// keyName returns the bubbletea name of the key s of the keybindings
// setting. It is the inverse of [herdrReviewrKeyName]. s is one character
// or a named key, alone or after "ctrl+" or "alt+", as in herdr-reviewr.
// The bubbletea names "pgup" and "pgdown" are also valid. ok is false if s
// is not a valid key.
func keyName(s string) (name string, ok bool) {
	mod, rest := cutModifier(s)
	named := func(k namedKey) bool { return rest == k.name || rest == k.tea }
	if i := slices.IndexFunc(namedKeys, named); i >= 0 {
		return mod + namedKeys[i].tea, true
	}
	r, size := utf8.DecodeRuneInString(rest)
	if size == 0 || size != len(rest) || r == utf8.RuneError || unicode.IsSpace(r) ||
		ansi.StringWidth(rest) == 0 {
		return "", false
	}
	return s, true
}

// herdrReviewrKeyName returns the bubbletea key name as herdr-reviewr
// spells it, for example "pageup" for "pgup". It is the inverse of
// [keyName].
func herdrReviewrKeyName(name string) string {
	if mod, k, ok := teaKey(name); ok {
		return mod + k.name
	}
	return name
}

// cutModifier returns the "ctrl+" or "alt+" at the start of s, if any, and
// the rest of s.
func cutModifier(s string) (mod, rest string) {
	for _, m := range []string{"ctrl+", "alt+"} {
		if r, ok := strings.CutPrefix(s, m); ok {
			return m, r
		}
	}
	return "", s
}

// keyNames lists the named keys for an error message.
func keyNames() string {
	names := make([]string, len(namedKeys))
	for i, k := range namedKeys {
		names[i] = k.name
	}
	return strings.Join(names, ", ")
}

// helpKeys implements help.KeyMap for the mode and the tab of the program.
type helpKeys struct {
	k      keyMap
	mode   mode
	tab    tab
	split  bool
	visual bool
	atFold bool // the cursor is on a fold
	armed  int  // the direction of an armed crossing, or 0
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
	case h.armed > 0:
		return []key.Binding{
			hint(k.NextHunk, firstKey(k.NextHunk), "next file"),
			k.Comment, k.Send, k.Keys, k.Quit,
		}
	case h.armed < 0:
		return []key.Binding{
			hint(k.PrevHunk, firstKey(k.PrevHunk), "prev file"),
			k.Comment, k.Send, k.Keys, k.Quit,
		}
	case h.visual:
		return []key.Binding{
			k.Comment,
			hint(k.Down, k.Down.Help().Key, "extend"),
			hint(k.Cancel, firstKey(k.Cancel), "clear"),
		}
	case h.tab == tabPR:
		return []key.Binding{k.Down, k.OpenPR, k.Refresh, k.TabChanges, k.Keys, k.Quit}
	case h.atFold:
		return []key.Binding{
			hint(k.Expand, firstKey(k.Expand), "expand fold"),
			k.Comment, k.Send, k.NextHunk, k.Keys, k.Quit,
		}
	}
	return []key.Binding{k.Comment, k.Send, k.NextHunk, k.NextFile, k.Split, k.Keys, k.Quit}
}

// FullHelp returns the columns of keys that ? shows.
func (h helpKeys) FullHelp() [][]key.Binding {
	k := h.k
	move := []key.Binding{
		k.Down, k.HalfDown, k.NextHunk, k.NextFile, k.Focus, k.Collapse, k.GotoLine,
	}
	scopes := []key.Binding{
		k.TabChanges, k.ScopeUncommitted, k.ScopeBranch, k.ScopeLastTurn, k.ScopeCommits,
		k.BasePick, k.CommitPick,
	}
	comments := []key.Binding{
		k.Select, k.Comment, k.Edit, k.Delete, k.NextComment, k.Comments, k.Send, k.Copy,
	}
	view := []key.Binding{
		k.Search, k.Find, k.Rendered, k.Split, k.Wrap, k.NavigatorPosition, k.NavigatorHide, k.NavigatorGrow,
	}
	other := []key.Binding{k.OpenPR, k.Refresh, k.Keys, k.Quit}
	return [][]key.Binding{move, scopes, comments, view, other}
}

// newHelp styles the footer with the terminal palette.
func newHelp() help.Model {
	h := help.New()
	h.Styles.ShortKey, h.Styles.FullKey = boldStyle, boldStyle
	h.Styles.ShortDesc, h.Styles.FullDesc = dimStyle, dimStyle
	h.Styles.ShortSeparator, h.Styles.FullSeparator = dimStyle, dimStyle
	h.Styles.Ellipsis = dimStyle
	h.ShortSeparator, h.FullSeparator = "    ", "    "
	return h
}

// firstKey returns the label of the first key of b, as herdr-reviewr
// shows it, or "".
func firstKey(b key.Binding) string {
	ks := b.Keys()
	if len(ks) == 0 {
		return ""
	}
	if mod, k, ok := teaKey(ks[0]); ok {
		return mod + k.label
	}
	return ks[0]
}
