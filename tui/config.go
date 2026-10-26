package tui

import (
	"cmp"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
)

// Layout is the layout of the diff.
type Layout string

// The layouts.
const (
	LayoutUnified Layout = "unified" // the old and the new lines in one column
	LayoutSplit   Layout = "split"   // the old lines beside the new lines
)

// Scope tells what the Changes tab compares with the working tree.
type Scope string

// The scopes, and what they compare with the working tree.
const (
	ScopeUncommitted Scope = "uncommitted" // HEAD
	ScopeBranch      Scope = "branch"      // the base branch
	ScopeLastTurn    Scope = "last-turn"   // the start of the last agent turn
)

// markdownView tells how the program shows a Markdown file.
type markdownView string

const (
	markdownViewSource   markdownView = "source"
	markdownViewRendered markdownView = "rendered"
)

// position is the place of the file list around the main pane.
type position string

const (
	positionRight  position = "right"
	positionBottom position = "bottom"
	positionLeft   position = "left"
	positionTop    position = "top"
)

// next returns the position after p, clockwise around the main pane, as
// the key p of herdr-reviewr moves it.
func (p position) next() position {
	switch p {
	case positionRight:
		return positionBottom
	case positionBottom:
		return positionLeft
	case positionLeft:
		return positionTop
	}
	return positionRight
}

// stacked reports whether the file list is above or below the main pane.
// Then the list splits the rows of the body, not the columns.
func (p position) stacked() bool { return p == positionTop || p == positionBottom }

// The values that the settings accept, in the order of the error messages.
var (
	layouts       = []Layout{LayoutUnified, LayoutSplit}
	configScopes  = []Scope{ScopeUncommitted, ScopeBranch, ScopeLastTurn}
	markdownViews = []markdownView{markdownViewSource, markdownViewRendered}
	positions     = []position{positionRight, positionBottom, positionLeft, positionTop}
)

// config holds the settings of rexdiff. The settings are in rexdiff.lua,
// beside the configuration file (init.lua) of Rex. The toggle action gives
// them to rexdiff as JSON in --config.
type config struct {
	Layout       Layout `json:"layout"`
	DefaultScope Scope  `json:"default_scope"`
	// ReviewPrefix is the text above each review that rexdiff sends or
	// copies.
	ReviewPrefix string `json:"review_prefix"`
	// Editor is a command, for example "code -g {file}:{line}".
	Editor string `json:"editor"`
	// URLOpener is a command, for example "open {url}".
	URLOpener    string       `json:"url_opener"`
	MarkdownView markdownView `json:"markdown_view"`
	// FileList is the place of the list of files: right, bottom, left, or
	// top.
	FileList position `json:"file_list"`
	// FileListWidth is the width of the file list at a side, in columns.
	// It is minListWidth or more, or 0 if the text does not set it.
	FileListWidth int `json:"file_list_width"`
	// DiffSigns marks a changed line with + or -, in place of a bar.
	DiffSigns bool `json:"diff_signs"`
	// FocusAgent is true if the focus moves to the agent after a send.
	// Nil means true.
	FocusAgent *bool `json:"focus_agent_after_send"`
	// GitHubHost, GitLabHost, and AzureHost are bare hostnames of
	// self-hosted forges.
	GitHubHost  string      `json:"github_host"`
	GitLabHost  string      `json:"gitlab_host"`
	AzureHost   string      `json:"azure_devops_host"`
	Keybindings keybindings `json:"keybindings"`
}

// keybindings sets the keys of each action. The JSON keys are the names of
// the actions. A nil list keeps the default keys.
type keybindings struct {
	// Move.
	Down     []string `json:"down"`
	Up       []string `json:"up"`
	PageDown []string `json:"page-down"`
	PageUp   []string `json:"page-up"`
	HalfDown []string `json:"half-down"`
	HalfUp   []string `json:"half-up"`
	Collapse []string `json:"collapse"`
	Expand   []string `json:"expand"`
	NextHunk []string `json:"next-hunk"`
	PrevHunk []string `json:"prev-hunk"`
	NextFile []string `json:"next-file"`
	PrevFile []string `json:"prev-file"`
	Focus    []string `json:"focus"`

	// Scopes and tabs.
	ScopeUncommitted []string `json:"scope-uncommitted"`
	ScopeBranch      []string `json:"scope-branch"`
	ScopeLastTurn    []string `json:"scope-last-turn"`
	ScopeCommits     []string `json:"scope-commits"`
	BasePick         []string `json:"base-pick"`
	CommitPick       []string `json:"commit-pick"`
	TabChanges       []string `json:"tab-changes"`
	TabAllFiles      []string `json:"tab-all-files"`
	TabPR            []string `json:"tab-pr"`

	// Comments.
	Select      []string `json:"select"`
	Comment     []string `json:"comment"`
	Edit        []string `json:"edit"`
	Delete      []string `json:"delete"`
	NextComment []string `json:"next-comment"`
	PrevComment []string `json:"prev-comment"`
	Comments    []string `json:"comments"`
	Send        []string `json:"send"`
	Copy        []string `json:"copy"`

	// View and other actions.
	Search            []string `json:"search"`
	Find              []string `json:"find"`
	GotoLine          []string `json:"goto-line"`
	Rendered          []string `json:"rendered"`
	Split             []string `json:"split"`
	Wrap              []string `json:"wrap"`
	NavigatorPosition []string `json:"navigator-position"`
	NavigatorHide     []string `json:"navigator-hide"`
	NavigatorGrow     []string `json:"navigator-grow"`
	NavigatorShrink   []string `json:"navigator-shrink"`
	OpenPR            []string `json:"open-pr"`
	Refresh           []string `json:"refresh"`
	Keys              []string `json:"keys"`
	Quit              []string `json:"quit"`
	QuitDiscard       []string `json:"quit-discard"`

	// The old names of navigator-grow, navigator-shrink, and rendered.
	// herdr-reviewr also takes them.
	ListWider    []string `json:"list-wider"`
	ListNarrower []string `json:"list-narrower"`
	Preview      []string `json:"preview"`
}

// useOldNames gives the keys of each old name to its action. An action
// and its old name cannot both have keys.
func (kb *keybindings) useOldNames() error {
	names := []struct {
		name, old     string
		keys, oldKeys *[]string
	}{
		{"navigator-grow", "list-wider", &kb.NavigatorGrow, &kb.ListWider},
		{"navigator-shrink", "list-narrower", &kb.NavigatorShrink, &kb.ListNarrower},
		{"rendered", "preview", &kb.Rendered, &kb.Preview},
	}
	for _, n := range names {
		switch {
		case *n.oldKeys == nil:
		case *n.keys != nil:
			return fmt.Errorf("invalid value for keybindings: %q and %q name the same action",
				n.name, n.old)
		default:
			*n.keys, *n.oldKeys = *n.oldKeys, nil
		}
	}
	return nil
}

// minListWidth is the smallest width of the file list, in columns.
const minListWidth = 16

// defaultConfig returns the value of each setting that --config does not
// set.
func defaultConfig() config {
	return config{
		Layout:       LayoutUnified,
		DefaultScope: ScopeUncommitted,
		MarkdownView: markdownViewSource,
		FileList:     positionRight,
	}
}

// parseConfig reads the JSON text over the default settings. An empty
// text gives the default settings. An unknown key, a key in the wrong
// case, or a bad value gives an error.
func parseConfig(text string) (config, error) {
	c := defaultConfig()
	if text == "" {
		return c, nil
	}
	// set tells which keys the text has. A blank text is an error only if
	// the user sets it.
	var set map[string]jsontext.Value
	if err := json.Unmarshal([]byte(text), &set); err != nil {
		return config{}, configError(err)
	}
	if err := json.Unmarshal([]byte(text), &c, json.RejectUnknownMembers(true)); err != nil {
		return config{}, configError(err)
	}
	if err := c.Keybindings.useOldNames(); err != nil {
		return config{}, fmt.Errorf("config: %w", err)
	}
	if err := c.check(set); err != nil {
		return config{}, fmt.Errorf("config: %w", err)
	}
	return c, nil
}

// configError changes an error of the JSON decoder into an error that
// uses the names of the settings, for example "keybindings.send".
func configError(err error) error {
	se, ok := errors.AsType[*json.SemanticError](err)
	if !ok {
		return fmt.Errorf("config: %w", err)
	}
	key := strings.Join(slices.Collect(se.JSONPointer.Tokens()), ".")
	if errors.Is(err, json.ErrUnknownName) {
		return fmt.Errorf("config: unknown key %q", key)
	}
	if key == "" || se.GoType == nil {
		return errors.New("config: expected a table of settings")
	}
	want := "a table"
	switch se.GoType.Kind() {
	case reflect.String:
		want = "text"
	case reflect.Int:
		want = "a whole number"
	case reflect.Bool:
		want = "true or false"
	case reflect.Slice:
		want = "a list of keys" // an empty Lua table encodes as a JSON object
	}
	return fmt.Errorf("config: invalid value for %s: expected %s, not %s",
		key, want, jsonKinds[se.JSONKind])
}

// jsonKinds names the kinds of JSON values in the words of Lua.
var jsonKinds = map[jsontext.Kind]string{
	'"': "text",
	'0': "a number",
	'{': "a table",
	'[': "a list",
	't': "a boolean",
	'f': "a boolean",
	'n': "nil",
}

// check returns an error for the first value that rexdiff does not accept.
// set has the keys that the text sets.
func (c config) check(set map[string]jsontext.Value) error {
	if err := c.checkChoices(set); err != nil {
		return err
	}
	if err := c.checkTexts(set); err != nil {
		return err
	}
	if err := c.checkHosts(set); err != nil {
		return err
	}
	return c.checkKeybindings()
}

// checkChoices checks the values that have fixed choices or a range. They
// are the layout, the scope, the markdown view, and the file list. set
// has the keys that the text sets.
func (c config) checkChoices(set map[string]jsontext.Value) error {
	if err := cmp.Or(
		choice("layout", c.Layout, layouts),
		choice("default_scope", c.DefaultScope, configScopes),
		choice("markdown_view", c.MarkdownView, markdownViews),
		choice("file_list", c.FileList, positions),
	); err != nil {
		return err
	}
	if set["file_list_width"] != nil && c.FileListWidth < minListWidth {
		want := strconv.Itoa(minListWidth) + " or more"
		return invalid("file_list_width", want, strconv.Itoa(c.FileListWidth))
	}
	return nil
}

// checkTexts checks the commands and the review prefix. A command can use
// only its placeholders. A text that the user sets must not be blank.
func (c config) checkTexts(set map[string]jsontext.Value) error {
	commands := []struct {
		key, value, want string
		known            []string
	}{
		{
			key:   "editor",
			value: c.Editor,
			want:  "a command with only {file} and {line}",
			known: []string{"file", "line"},
		},
		{
			key:   "url_opener",
			value: c.URLOpener,
			want:  "a command with only {url}",
			known: []string{"url"},
		},
	}
	for _, k := range commands {
		blank := set[k.key] != nil && strings.TrimSpace(k.value) == ""
		if blank || !onlyPlaceholders(k.value, k.known...) {
			return invalid(k.key, k.want, k.value)
		}
	}
	// As in herdr-reviewr, the first word of the URL opener is a program,
	// and not the URL.
	if w := splitWords(c.URLOpener); c.URLOpener != "" &&
		(len(w) == 0 || w[0] == "" || strings.Contains(w[0], "{url}")) {
		return invalid("url_opener", "a command that names a program first", c.URLOpener)
	}
	if set["review_prefix"] != nil && strings.TrimSpace(c.ReviewPrefix) == "" {
		return invalid("review_prefix", "text", c.ReviewPrefix)
	}
	return nil
}

// checkHosts checks the hostnames of the self-hosted forges.
func (c config) checkHosts(set map[string]jsontext.Value) error {
	hosts := []struct{ key, value string }{
		{"github_host", c.GitHubHost},
		{"gitlab_host", c.GitLabHost},
		{"azure_devops_host", c.AzureHost},
	}
	for _, k := range hosts {
		if set[k.key] != nil && !isHostname(k.value) {
			return invalid(k.key, "a bare hostname", k.value)
		}
	}
	return nil
}

// checkKeybindings checks the keys of the actions, as herdr-reviewr does.
// The decoder rejects an unknown action. A list that is set must have
// keys, and each key must have a valid name. Then no key can have two
// actions, after the setting changes the default keys.
func (c config) checkKeybindings() error {
	k := defaultKeys()
	bs := k.bindings(c.Keybindings)
	for _, b := range bs {
		if b.keys == nil {
			continue
		}
		want := "a list of keys, each a character or a named key (" + keyNames() +
			"), alone or after ctrl+ or alt+"
		if len(b.keys) == 0 {
			return invalid("keybindings."+b.name, want, "[]")
		}
		for _, s := range b.keys {
			if _, ok := keyName(s); !ok {
				return invalid("keybindings."+b.name, want, s)
			}
		}
	}
	k.applyConfig(c.Keybindings)
	first := map[string]string{} // the first action of each key
	for _, b := range bs {
		for _, key := range b.key.Keys() {
			switch a, ok := first[key]; {
			case ok && a == b.name:
				return fmt.Errorf("invalid value for keybindings: %q is bound twice to %q",
					herdrReviewrKeyName(key), b.name)
			case ok:
				return fmt.Errorf("invalid value for keybindings: %q is bound to both %q and %q",
					herdrReviewrKeyName(key), a, b.name)
			}
			first[key] = b.name
		}
	}
	return nil
}

// invalid returns the error for a value of the setting key that rexdiff
// does not accept. want tells the values that rexdiff accepts.
func invalid(key, want, value string) error {
	return fmt.Errorf("invalid value for %s: expected %s, not %q", key, want, value)
}

// onlyPlaceholders reports whether each { in command opens one of the
// known placeholders, for example {file}.
func onlyPlaceholders(command string, known ...string) bool {
	for _, rest, ok := strings.Cut(command, "{"); ok; _, rest, ok = strings.Cut(rest, "{") {
		name, _, closed := strings.Cut(rest, "}")
		if !closed || !slices.Contains(known, name) {
			return false
		}
	}
	return true
}

// The longest DNS name and the longest label of a DNS name (RFC 1035).
const (
	maxHostname  = 253
	maxHostLabel = 63
)

// isHostname reports whether s is a bare DNS name, for example
// "git.example.com", with no scheme, port, or path.
func isHostname(s string) bool {
	if len(s) > maxHostname {
		return false
	}
	for label := range strings.SplitSeq(s, ".") {
		if !isHostLabel(label) {
			return false
		}
	}
	return true
}

// isHostLabel reports whether s is one valid label of a DNS name.
func isHostLabel(s string) bool {
	if s == "" || len(s) > maxHostLabel || s[0] == '-' || s[len(s)-1] == '-' {
		return false
	}
	return !strings.ContainsFunc(s, func(r rune) bool {
		letter := 'a' <= r && r <= 'z' || 'A' <= r && r <= 'Z'
		return !letter && !('0' <= r && r <= '9') && r != '-'
	})
}

// focusesAgent reports whether the focus moves to the agent after a send.
func (c config) focusesAgent() bool { return c.FocusAgent == nil || *c.FocusAgent }

// choice returns an error if value is not one of allowed. key is the name
// of the value in the error.
func choice[T ~string](key string, value T, allowed []T) error {
	if slices.Contains(allowed, value) {
		return nil
	}
	want := make([]string, len(allowed))
	for i, a := range allowed {
		want[i] = string(a)
	}
	return invalid(key, "one of "+strings.Join(want, ", "), string(value))
}
