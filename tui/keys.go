package tui

import (
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// keyMap holds the key binding for each action. Each field is named after
// the action in the keybindings setting, such as NextHunk for next-hunk
// (see [keyActions]). The settings cannot change the keys of the comment
// box or the find bar.
type keyMap struct {
	// Movement.
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
	// Update installs a newer release. It works only while the footer is
	// announcing one.
	Update key.Binding

	// Keys of the comment box. Cancel also clears the selection.
	SaveComment, InsertNewline, Cancel key.Binding
}

// bind returns a binding for keys, with helpKey and desc as its help text.
func bind(keys []string, helpKey, desc string) key.Binding {
	return key.NewBinding(key.WithKeys(keys...), key.WithHelp(helpKey, desc))
}

// hint returns a copy of b with different help text. The copy keeps the
// keys of b, because the help view hides bindings that have no keys.
func hint(b key.Binding, helpKey, desc string) key.Binding {
	b.SetHelp(helpKey, desc)
	return b
}

// keyAction describes an action that can be bound to keys.
type keyAction struct {
	// name is the action name in the keybindings setting, or "" for a
	// key of the comment box.
	name          string
	keys          []string // the default keys
	helpKey, help string
	field         func(*keyMap) *key.Binding
	// setting returns the keys that the keybindings setting assigns to
	// the action. It is nil if the setting cannot change them.
	setting func(*keybindings) []string
	// handle runs the action when no box is open. It is nil if the key
	// does nothing in that state. The QuitDiscard key only answers the
	// quit prompt.
	handle func(*model) tea.Cmd
	// keepView reports whether the view stays put, rather than scrolling
	// to the cursor, after the action runs.
	keepView bool
}

// keyActions lists every key action, in the order that keybinding errors
// name them. The update action, whose key is fixed, follows the
// configurable actions, and the comment box keys come last. A help label
// shared by a group, such as "j/k move", belongs to its first action (see
// [keyMap.pairs]).
var keyActions = []keyAction{
	{
		name: "down",
		keys: []string{"j", "down"}, helpKey: "j/k", help: "move",
		field:   func(k *keyMap) *key.Binding { return &k.Down },
		setting: func(c *keybindings) []string { return c.Down },
		handle:  (*model).onDown,
	},
	{
		name: "up",
		keys: []string{"k", "up"}, helpKey: "k", help: "up",
		field:   func(k *keyMap) *key.Binding { return &k.Up },
		setting: func(c *keybindings) []string { return c.Up },
		handle:  (*model).onUp,
	},
	{
		name: "next-hunk",
		keys: []string{"]"}, helpKey: "]/[", help: "next/prev change",
		field:   func(k *keyMap) *key.Binding { return &k.NextHunk },
		setting: func(c *keybindings) []string { return c.NextHunk },
		handle:  (*model).onNextHunk,
	},
	{
		name: "prev-hunk",
		keys: []string{"["}, helpKey: "[", help: "prev change",
		field:   func(k *keyMap) *key.Binding { return &k.PrevHunk },
		setting: func(c *keybindings) []string { return c.PrevHunk },
		handle:  (*model).onPrevHunk,
	},
	{
		name: "next-file",
		keys: []string{"f"}, helpKey: "f/F", help: "next/prev file",
		field:   func(k *keyMap) *key.Binding { return &k.NextFile },
		setting: func(c *keybindings) []string { return c.NextFile },
		handle:  (*model).onNextFile,
	},
	{
		name: "prev-file",
		keys: []string{"F"}, helpKey: "F", help: "prev file",
		field:   func(k *keyMap) *key.Binding { return &k.PrevFile },
		setting: func(c *keybindings) []string { return c.PrevFile },
		handle:  (*model).onPrevFile,
	},
	{
		name: "collapse",
		keys: []string{"left"}, helpKey: "←/→", help: "scroll sideways",
		field:   func(k *keyMap) *key.Binding { return &k.Collapse },
		setting: func(c *keybindings) []string { return c.Collapse },
		handle:  (*model).onCollapse,
	},
	{
		name: "expand",
		keys: []string{"right"}, helpKey: "→", help: "scroll right",
		field:   func(k *keyMap) *key.Binding { return &k.Expand },
		setting: func(c *keybindings) []string { return c.Expand },
		handle:  (*model).onExpand,
	},
	{
		name: "page-up",
		keys: []string{"pgup"}, helpKey: "pgup", help: "page up",
		field:   func(k *keyMap) *key.Binding { return &k.PageUp },
		setting: func(c *keybindings) []string { return c.PageUp },
		handle:  (*model).onPageUp,
	},
	{
		name: "page-down",
		keys: []string{"pgdown"}, helpKey: "pgdn", help: "page down",
		field:   func(k *keyMap) *key.Binding { return &k.PageDown },
		setting: func(c *keybindings) []string { return c.PageDown },
		handle:  (*model).onPageDown,
	},
	{
		name: "half-up",
		keys: []string{"ctrl+u"}, helpKey: "ctrl+u", help: "half page up",
		field:   func(k *keyMap) *key.Binding { return &k.HalfUp },
		setting: func(c *keybindings) []string { return c.HalfUp },
		handle:  (*model).onHalfUp,
	},
	{
		name: "half-down",
		keys: []string{"ctrl+d"}, helpKey: "ctrl+d/u", help: "half page",
		field:   func(k *keyMap) *key.Binding { return &k.HalfDown },
		setting: func(c *keybindings) []string { return c.HalfDown },
		handle:  (*model).onHalfDown,
	},
	{
		name: "focus",
		keys: []string{"tab"}, helpKey: "tab", help: "switch pane",
		field:   func(k *keyMap) *key.Binding { return &k.Focus },
		setting: func(c *keybindings) []string { return c.Focus },
		handle:  (*model).onFocus,
	},
	{
		name: "scope-uncommitted",
		keys: []string{"u"}, helpKey: "u", help: "uncommitted",
		field:   func(k *keyMap) *key.Binding { return &k.ScopeUncommitted },
		setting: func(c *keybindings) []string { return c.ScopeUncommitted },
		handle:  (*model).onScopeUncommitted,
	},
	{
		name: "scope-branch",
		keys: []string{"b"}, helpKey: "b", help: "branch",
		field:   func(k *keyMap) *key.Binding { return &k.ScopeBranch },
		setting: func(c *keybindings) []string { return c.ScopeBranch },
		handle:  (*model).onScopeBranch,
	},
	{
		name: "scope-last-turn",
		keys: []string{"t"}, helpKey: "t", help: "last turn",
		field:   func(k *keyMap) *key.Binding { return &k.ScopeLastTurn },
		setting: func(c *keybindings) []string { return c.ScopeLastTurn },
		handle:  (*model).onScopeLastTurn,
	},
	{
		name: "scope-commits",
		keys: []string{"g"}, helpKey: "g", help: "commits",
		field:   func(k *keyMap) *key.Binding { return &k.ScopeCommits },
		setting: func(c *keybindings) []string { return c.ScopeCommits },
		handle:  (*model).onScopeCommits,
	},
	{
		name: "base-pick",
		keys: []string{"B"}, helpKey: "B", help: "pick base branch",
		field:   func(k *keyMap) *key.Binding { return &k.BasePick },
		setting: func(c *keybindings) []string { return c.BasePick },
		handle:  (*model).onBasePick,
	},
	{
		name: "commit-pick",
		keys: []string{"G"}, helpKey: "G", help: "pick commits",
		field:   func(k *keyMap) *key.Binding { return &k.CommitPick },
		setting: func(c *keybindings) []string { return c.CommitPick },
		handle:  (*model).onCommitPick,
	},
	{
		name: "tab-changes",
		keys: []string{"1"}, helpKey: "1/2/3", help: "changes/files/PR",
		field:   func(k *keyMap) *key.Binding { return &k.TabChanges },
		setting: func(c *keybindings) []string { return c.TabChanges },
		handle:  (*model).onTabChanges,
	},
	{
		name: "tab-all-files",
		keys: []string{"2"}, helpKey: "2", help: "all files",
		field:   func(k *keyMap) *key.Binding { return &k.TabAllFiles },
		setting: func(c *keybindings) []string { return c.TabAllFiles },
		handle:  (*model).onTabAllFiles,
	},
	{
		name: "tab-pr",
		keys: []string{"3"}, helpKey: "3", help: "PR",
		field:   func(k *keyMap) *key.Binding { return &k.TabPR },
		setting: func(c *keybindings) []string { return c.TabPR },
		handle:  (*model).onTabPR,
	},
	{
		name: "wrap",
		keys: []string{"w"}, helpKey: "w", help: "wrap lines",
		field:   func(k *keyMap) *key.Binding { return &k.Wrap },
		setting: func(c *keybindings) []string { return c.Wrap },
		handle:  (*model).onWrap,
	},
	{
		name: "rendered",
		keys: []string{"m"}, helpKey: "m", help: "rendered markdown",
		field:   func(k *keyMap) *key.Binding { return &k.Rendered },
		setting: func(c *keybindings) []string { return c.Rendered },
		handle:  (*model).onRendered,
	},
	{
		name: "split",
		keys: []string{"|"}, helpKey: "|", help: "side by side",
		field:   func(k *keyMap) *key.Binding { return &k.Split },
		setting: func(c *keybindings) []string { return c.Split },
		handle:  (*model).onSplit,
	},
	{
		name: "navigator-position",
		keys: []string{"p"}, helpKey: "p", help: "move list",
		field:   func(k *keyMap) *key.Binding { return &k.NavigatorPosition },
		setting: func(c *keybindings) []string { return c.NavigatorPosition },
		handle:  (*model).onNavigatorPosition,
	},
	{
		name: "navigator-hide",
		keys: []string{"z"}, helpKey: "z", help: "hide list",
		field:   func(k *keyMap) *key.Binding { return &k.NavigatorHide },
		setting: func(c *keybindings) []string { return c.NavigatorHide },
		handle:  (*model).onNavigatorHide,
	},
	{
		name: "navigator-grow",
		keys: []string{"<"}, helpKey: "</>", help: "resize list",
		field:   func(k *keyMap) *key.Binding { return &k.NavigatorGrow },
		setting: func(c *keybindings) []string { return c.NavigatorGrow },
		handle:  (*model).onNavigatorGrow,
	},
	{
		name: "navigator-shrink",
		keys: []string{">"}, helpKey: ">", help: "narrow list",
		field:   func(k *keyMap) *key.Binding { return &k.NavigatorShrink },
		setting: func(c *keybindings) []string { return c.NavigatorShrink },
		handle:  (*model).onNavigatorShrink,
	},
	{
		name: "select",
		keys: []string{"v"}, helpKey: "v", help: "select lines",
		field:   func(k *keyMap) *key.Binding { return &k.Select },
		setting: func(c *keybindings) []string { return c.Select },
		handle:  (*model).onSelect,
	},
	{
		name: "comment",
		keys: []string{"c"}, helpKey: "c", help: "comment",
		field:   func(k *keyMap) *key.Binding { return &k.Comment },
		setting: func(c *keybindings) []string { return c.Comment },
		handle:  (*model).onComment,
	},
	{
		name: "edit",
		keys: []string{"e"}, helpKey: "e", help: "edit comment/open editor",
		field:   func(k *keyMap) *key.Binding { return &k.Edit },
		setting: func(c *keybindings) []string { return c.Edit },
		handle:  (*model).onEdit,
	},
	{
		name: "delete",
		keys: []string{"d"}, helpKey: "d", help: "delete comment",
		field:   func(k *keyMap) *key.Binding { return &k.Delete },
		setting: func(c *keybindings) []string { return c.Delete },
		handle:  (*model).onDelete,
	},
	{
		name: "next-comment",
		keys: []string{"n"}, helpKey: "n/N", help: "next/prev comment",
		field:   func(k *keyMap) *key.Binding { return &k.NextComment },
		setting: func(c *keybindings) []string { return c.NextComment },
		handle:  (*model).onNextComment,
	},
	{
		name: "prev-comment",
		keys: []string{"N"}, helpKey: "N", help: "prev comment",
		field:   func(k *keyMap) *key.Binding { return &k.PrevComment },
		setting: func(c *keybindings) []string { return c.PrevComment },
		handle:  (*model).onPrevComment,
	},
	{
		name: "comments",
		keys: []string{"l"}, helpKey: "l", help: "list comments",
		field:   func(k *keyMap) *key.Binding { return &k.Comments },
		setting: func(c *keybindings) []string { return c.Comments },
		handle:  (*model).onComments,
	},
	{
		name: "search",
		keys: []string{"/"}, helpKey: "/", help: "search files and code",
		field:   func(k *keyMap) *key.Binding { return &k.Search },
		setting: func(c *keybindings) []string { return c.Search },
		handle:  (*model).onSearch,
	},
	{
		name: "find",
		keys: []string{"ctrl+f"}, helpKey: "ctrl+f", help: "find in file",
		field:   func(k *keyMap) *key.Binding { return &k.Find },
		setting: func(c *keybindings) []string { return c.Find },
		handle:  (*model).onFind,
	},
	{
		name: "goto-line",
		keys: []string{":"}, helpKey: ":", help: "go to line",
		field:   func(k *keyMap) *key.Binding { return &k.GotoLine },
		setting: func(c *keybindings) []string { return c.GotoLine },
		handle:  (*model).onGotoLine,
	},
	{
		name: "keys",
		keys: []string{"?"}, helpKey: "?", help: "all keys",
		field:   func(k *keyMap) *key.Binding { return &k.Keys },
		setting: func(c *keybindings) []string { return c.Keys },
		handle:  (*model).onKeys,
	},
	{
		name: "send",
		keys: []string{"s", "S"}, helpKey: "s", help: "send to agent",
		field:   func(k *keyMap) *key.Binding { return &k.Send },
		setting: func(c *keybindings) []string { return c.Send },
		handle:  (*model).onSend,
	},
	{
		name: "copy",
		keys: []string{"y", "Y"}, helpKey: "y", help: "copy comments",
		field:   func(k *keyMap) *key.Binding { return &k.Copy },
		setting: func(c *keybindings) []string { return c.Copy },
		handle:  (*model).onCopy,
	},
	{
		name: "open-pr",
		keys: []string{"o"}, helpKey: "o", help: "open PR in browser",
		field:   func(k *keyMap) *key.Binding { return &k.OpenPR },
		setting: func(c *keybindings) []string { return c.OpenPR },
		handle:  (*model).onOpenPR,
	},
	{
		name: "refresh",
		keys: []string{"r"}, helpKey: "r", help: "refresh",
		field:   func(k *keyMap) *key.Binding { return &k.Refresh },
		setting: func(c *keybindings) []string { return c.Refresh },
		handle:  (*model).onRefresh,
	},
	{
		name: "quit",
		keys: []string{"q"}, helpKey: "q", help: "quit",
		field:    func(k *keyMap) *key.Binding { return &k.Quit },
		setting:  func(c *keybindings) []string { return c.Quit },
		handle:   (*model).onQuit,
		keepView: true,
	},
	{
		name: "quit-discard",
		keys: []string{"Q"}, helpKey: "Q", help: "quit, drop comments",
		field:   func(k *keyMap) *key.Binding { return &k.QuitDiscard },
		setting: func(c *keybindings) []string { return c.QuitDiscard },
	},
	{
		name: "update",
		keys: []string{"U"}, helpKey: "U", help: "update rexdiff",
		field:  func(k *keyMap) *key.Binding { return &k.Update },
		handle: (*model).onUpdate,
	},
	{
		keys: []string{"enter"}, helpKey: "enter", help: "save",
		field: func(k *keyMap) *key.Binding { return &k.SaveComment },
	},
	{
		keys: []string{"shift+enter", "alt+enter", "ctrl+j"}, helpKey: "shift+enter", help: "new line",
		field: func(k *keyMap) *key.Binding { return &k.InsertNewline },
	},
	{
		keys: []string{"esc"}, helpKey: "esc", help: "cancel",
		field:  func(k *keyMap) *key.Binding { return &k.Cancel },
		handle: (*model).onCancel,
	},
}

// defaultKeys returns the default key bindings, with the help text for
// each action.
func defaultKeys() keyMap {
	var k keyMap
	for _, a := range keyActions {
		*a.field(&k) = bind(a.keys, a.helpKey, a.help)
	}
	return k
}

// action returns the action that msg triggers when no box is open, or
// nil. The order of the actions does not matter, because checking the
// keybindings setting ensures that each key has at most one action.
func (k *keyMap) action(msg tea.KeyPressMsg) *keyAction {
	for i, a := range keyActions {
		if a.handle != nil && key.Matches(msg, *a.field(k)) {
			return &keyActions[i]
		}
	}
	return nil
}

// overlayKeyMap holds the keys used in overlays. The settings cannot
// change them.
type overlayKeyMap struct {
	// Up and Down move the cursor through the items. Their letter keys, k
	// and j, move the cursor only in an overlay without a text input, such
	// as the agent picker. A text input types them instead (see
	// [overlay.isCursorKey]).
	Choose, Close, Up, Down key.Binding

	// ToggleSearchMode switches the search mode. ScrollPreviewUp and
	// ScrollPreviewDown scroll the search preview.
	ToggleSearchMode, ScrollPreviewUp, ScrollPreviewDown key.Binding
}

// overlayKeys holds the keys used in overlays.
var overlayKeys = overlayKeyMap{
	Choose:            key.NewBinding(key.WithKeys("enter")),
	Close:             key.NewBinding(key.WithKeys("esc")),
	Up:                key.NewBinding(key.WithKeys("up", "ctrl+p", "ctrl+k", "k")),
	Down:              key.NewBinding(key.WithKeys("down", "ctrl+n", "ctrl+j", "j")),
	ToggleSearchMode:  key.NewBinding(key.WithKeys("tab")),
	ScrollPreviewUp:   key.NewBinding(key.WithKeys("pgup")),
	ScrollPreviewDown: key.NewBinding(key.WithKeys("pgdown")),
}

// letterMoveKeys are the keys of overlayKeys.Up and Down that are also
// letters.
var letterMoveKeys = []string{"j", "k"}

// binding pairs an action from the keybindings setting with the key
// binding that it changes.
type binding struct {
	name string // the action name in the keybindings setting
	keys []string
	key  *key.Binding
}

// bindings returns each configurable action with the keys that c sets for
// it and its binding in k, in the order of keyActions.
func (k *keyMap) bindings(c keybindings) []binding {
	var bs []binding
	for _, a := range keyActions {
		if a.name == "" {
			continue
		}
		var keys []string
		if a.setting != nil {
			keys = a.setting(&c)
		}
		bs = append(bs, binding{a.name, keys, a.field(k)})
	}
	return bs
}

// applyConfig sets the keys of each action that c configures. c must
// already have passed validation. The help for a changed action shows its
// first key, and a shared label such as "j/k move" shows the first key of
// each action in the group.
func (k *keyMap) applyConfig(c keybindings) {
	changed := map[*key.Binding]bool{}
	for _, b := range k.bindings(c) {
		if len(b.keys) == 0 {
			continue
		}
		keys := make([]string, len(b.keys))
		for i, s := range b.keys {
			keys[i], _ = keyName(s) // cannot fail: c has passed validation
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

// pairs returns the groups of actions that share one help label, such as
// "j/k move". The label belongs to the first action of each group.
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

// namedKeys are the keys that are not a single character. Each has its
// name in the keybindings setting, its bubbletea name and its on-screen
// label.
var namedKeys = []namedKey{
	{"left", "left", "←"},
	{"right", "right", "→"},
	{"up", "up", "↑"},
	{"down", "down", "↓"},
	{"pageup", "pgup", "PageUp"},
	{"pagedown", "pgdown", "PageDown"},
}

// namedKey is a key that is not a single character.
type namedKey struct{ name, tea, label string }

// teaKey splits the bubbletea key name into its modifier and the named
// key it refers to. ok is false if no named key matches.
func teaKey(name string) (mod string, k namedKey, ok bool) {
	mod, rest := cutModifier(name)
	i := slices.IndexFunc(namedKeys, func(nk namedKey) bool { return nk.tea == rest })
	if i < 0 {
		return mod, namedKey{}, false
	}
	return mod, namedKeys[i], true
}

// keyName converts the key s from the keybindings setting to its
// bubbletea name. It is the inverse of [settingKeyName]. s is a
// single character or a named key, optionally after "ctrl+" or "alt+".
// The bubbletea names "pgup" and "pgdown" are also accepted. ok is false
// if s is not a valid key.
func keyName(s string) (name string, ok bool) {
	mod, rest := cutModifier(s)
	named := func(k namedKey) bool { return rest == k.name || rest == k.tea }
	if i := slices.IndexFunc(namedKeys, named); i >= 0 {
		return mod + namedKeys[i].tea, true
	}
	r, size := utf8.DecodeRuneInString(rest)
	oneRune := size > 0 && size == len(rest) && r != utf8.RuneError
	if !oneRune || unicode.IsSpace(r) || ansi.StringWidth(rest) == 0 {
		return "", false
	}
	return s, true
}

// settingKeyName converts a bubbletea key name to its name in the
// keybindings setting, such as "pageup" for "pgup". It is the inverse of
// [keyName].
func settingKeyName(name string) string {
	if mod, k, ok := teaKey(name); ok {
		return mod + k.name
	}
	return name
}

// cutModifier splits a leading "ctrl+" or "alt+" from s. mod is "" if s
// has neither.
func cutModifier(s string) (mod, rest string) {
	for _, m := range []string{"ctrl+", "alt+"} {
		if r, ok := strings.CutPrefix(s, m); ok {
			return m, r
		}
	}
	return "", s
}

// keyNames returns a comma-separated list of the named keys, for use in
// error messages.
func keyNames() string {
	names := make([]string, len(namedKeys))
	for i, k := range namedKeys {
		names[i] = k.name
	}
	return strings.Join(names, ", ")
}

// helpKeys implements [help.KeyMap] for the current mode and tab.
type helpKeys struct {
	keys                 keyMap
	mode                 mode
	tab                  tab
	split                bool
	selecting            bool
	cursorOnFold         bool // the cursor is on a fold
	cursorInExpandedFold bool // the collapse key would collapse the expanded fold under the cursor
	crossStep            int  // the direction of a pending jump to another file, or 0
}

// ShortHelp returns the keys shown in the footer.
func (h helpKeys) ShortHelp() []key.Binding {
	k := h.keys
	if h.split {
		k.Split.SetHelp(k.Split.Help().Key, "top to bottom")
	}
	switch {
	case h.mode == modeComment:
		return []key.Binding{k.SaveComment, k.InsertNewline, k.Cancel}
	case h.crossStep != 0:
		b, label := k.NextHunk, "next file"
		if h.crossStep < 0 {
			b, label = k.PrevHunk, "prev file"
		}
		return []key.Binding{hint(b, firstKey(b), label), k.Comment, k.Send, k.Keys, k.Quit}
	case h.selecting:
		return []key.Binding{
			k.Comment,
			hint(k.Down, k.Down.Help().Key, "extend"),
			hint(k.Cancel, firstKey(k.Cancel), "clear"),
		}
	case h.tab == tabPR:
		return []key.Binding{k.Down, k.OpenPR, k.Refresh, k.TabChanges, k.Keys, k.Quit}
	case h.cursorOnFold || h.cursorInExpandedFold:
		b, label := k.Expand, "expand fold"
		if !h.cursorOnFold {
			b, label = k.Collapse, "collapse fold"
		}
		return []key.Binding{
			hint(b, firstKey(b), label),
			k.Comment, k.Send, k.NextHunk, k.Keys, k.Quit,
		}
	}
	return []key.Binding{k.Comment, k.Send, k.NextHunk, k.NextFile, k.Split, k.Keys, k.Quit}
}

// FullHelp returns the columns of keys shown by the ? key.
func (h helpKeys) FullHelp() [][]key.Binding {
	k := h.keys
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

// helpGap separates the keys in the short help. The header uses the same
// gap between scopes.
const helpGap = "  "

// newHelp returns the footer help, styled with the terminal palette.
func newHelp() help.Model {
	h := help.New()
	h.Styles.ShortKey, h.Styles.FullKey = boldStyle, boldStyle
	h.Styles.ShortDesc, h.Styles.FullDesc = dimStyle, dimStyle
	h.Styles.ShortSeparator, h.Styles.FullSeparator = dimStyle, dimStyle
	h.Styles.Ellipsis = dimStyle
	h.ShortSeparator, h.FullSeparator = helpGap, "    "
	return h
}

// firstKey returns the on-screen label of the first key of b, or "" if b
// has no keys.
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
