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

// Layout is the way a diff is laid out on screen.
type Layout string

// The supported layouts.
const (
	LayoutUnified Layout = "unified" // old and new lines in one column
	LayoutSplit   Layout = "split"   // old lines on the left, new lines on the right
)

// Scope selects what the Changes tab compares the working tree with.
type Scope string

// The scopes that can be set in the config. Each comment says what the
// working tree is compared with.
const (
	ScopeUncommitted Scope = "uncommitted" // HEAD
	ScopeBranch      Scope = "branch"      // the base branch
	ScopeLastTurn    Scope = "last-turn"   // the start of the last agent turn
)

// markdownView selects whether a Markdown file is shown as source or
// rendered.
type markdownView string

const (
	markdownViewSource   markdownView = "source"
	markdownViewRendered markdownView = "rendered"
)

// position is where the file list sits relative to the main pane.
type position string

const (
	positionRight  position = "right"
	positionBottom position = "bottom"
	positionLeft   position = "left"
	positionTop    position = "top"
)

// next returns the position after p, clockwise around the main pane.
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

// stacked reports whether the file list is above or below the main pane,
// in which case it takes rows from the body rather than columns.
func (p position) stacked() bool { return p == positionTop || p == positionBottom }

// The values that each setting accepts, in the order the error messages
// list them.
var (
	layouts       = []Layout{LayoutUnified, LayoutSplit}
	configScopes  = []Scope{ScopeUncommitted, ScopeBranch, ScopeLastTurn}
	markdownViews = []markdownView{markdownViewSource, markdownViewRendered}
	positions     = []position{positionRight, positionBottom, positionLeft, positionTop}
)

// config holds the rexdiff settings. The toggle action reads them from
// rexdiff.lua, in the same directory as the Rex init.lua, and passes them
// as JSON in --config.
type config struct {
	Layout       Layout `json:"layout"`
	DefaultScope Scope  `json:"default_scope"`
	// ReviewPrefix is the text that starts each sent or copied review.
	ReviewPrefix string `json:"review_prefix"`
	// Editor is the command that opens a file, such as
	// "code -g {file}:{line}".
	Editor string `json:"editor"`
	// URLOpener is the command that opens a URL, such as "open {url}".
	URLOpener    string       `json:"url_opener"`
	MarkdownView markdownView `json:"markdown_view"`
	// FileList is where the file list goes: right, bottom, left or top.
	FileList position `json:"file_list"`
	// FileListWidth is the width in columns of the file list when it is at
	// the left or right. It is at least minNavigatorWidth, or 0 if not set.
	FileListWidth int `json:"file_list_width"`
	// DiffSigns marks changed lines with + or - instead of a coloured bar.
	DiffSigns bool `json:"diff_signs"`
	// FocusAgent reports whether focus moves to the agent after sending a
	// review. Nil means true.
	FocusAgent *bool `json:"focus_agent_after_send"`
	// GitHubHost, GitLabHost and AzureHost are the bare hostnames of
	// self-hosted forges.
	GitHubHost  string      `json:"github_host"`
	GitLabHost  string      `json:"gitlab_host"`
	AzureHost   string      `json:"azure_devops_host"`
	Keybindings keybindings `json:"keybindings"`
}

// keybindings holds the keys bound to each action. The JSON names are the
// action names. A nil list keeps the default keys.
type keybindings struct {
	// Movement.
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

	// Former names of navigator-grow, navigator-shrink and rendered.
	ListWider    []string `json:"list-wider"`
	ListNarrower []string `json:"list-narrower"`
	Preview      []string `json:"preview"`
}

// useOldNames moves keys set under a former action name to the current
// name. It returns an error if both names have keys.
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

// minNavigatorWidth is the smallest allowed width of the file list, in
// columns.
const minNavigatorWidth = 16

// defaultConfig returns the settings used for anything --config does not
// set.
func defaultConfig() config {
	return config{
		Layout:       LayoutUnified,
		DefaultScope: ScopeUncommitted,
		MarkdownView: markdownViewSource,
		FileList:     positionRight,
	}
}

// parseConfig decodes the JSON text on top of the default settings. An
// empty text returns the defaults. It returns an error for an unknown key,
// a key in the wrong case, or an invalid value.
func parseConfig(text string) (config, error) {
	c := defaultConfig()
	if text == "" {
		return c, nil
	}
	// set records which keys the text sets, so that a blank value is an
	// error only when the user sets it.
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
	if err := c.validate(set); err != nil {
		return config{}, fmt.Errorf("config: %w", err)
	}
	return c, nil
}

// configError converts a JSON decoding error into one that names the
// setting, such as "keybindings.send".
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

// jsonKinds describes each kind of JSON value in Lua terms.
var jsonKinds = map[jsontext.Kind]string{
	'"': "text",
	'0': "a number",
	'{': "a table",
	'[': "a list",
	't': "a boolean",
	'f': "a boolean",
	'n': "nil",
}

// validate returns an error for the first invalid setting. set holds the
// keys that the config text sets.
func (c config) validate(set map[string]jsontext.Value) error {
	if err := c.validateChoices(set); err != nil {
		return err
	}
	if err := c.validateTexts(set); err != nil {
		return err
	}
	if err := c.validateHosts(set); err != nil {
		return err
	}
	return c.validateKeybindings()
}

// validateChoices checks the layout, default scope, Markdown view and file
// list settings. set holds the keys that the config text sets.
func (c config) validateChoices(set map[string]jsontext.Value) error {
	if err := cmp.Or(
		validateChoice("layout", c.Layout, layouts),
		validateChoice("default_scope", c.DefaultScope, configScopes),
		validateChoice("markdown_view", c.MarkdownView, markdownViews),
		validateChoice("file_list", c.FileList, positions),
	); err != nil {
		return err
	}
	if set["file_list_width"] != nil && c.FileListWidth < minNavigatorWidth {
		want := strconv.Itoa(minNavigatorWidth) + " or more"
		return invalidError("file_list_width", want, strconv.Itoa(c.FileListWidth))
	}
	return nil
}

// validateTexts checks the commands and the review prefix. A command may
// use only its own placeholders, and a value that the user sets must not
// be blank.
func (c config) validateTexts(set map[string]jsontext.Value) error {
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
		if blank || !hasOnlyPlaceholders(k.value, k.known...) {
			return invalidError(k.key, k.want, k.value)
		}
	}
	// The first word of the URL opener must name a program, not the URL.
	w := splitWords(c.URLOpener)
	noProgram := len(w) == 0 || w[0] == "" || strings.Contains(w[0], "{url}")
	if c.URLOpener != "" && noProgram {
		return invalidError("url_opener", "a command that names a program first", c.URLOpener)
	}
	if set["review_prefix"] != nil && strings.TrimSpace(c.ReviewPrefix) == "" {
		return invalidError("review_prefix", "text", c.ReviewPrefix)
	}
	return nil
}

// validateHosts checks the hostnames of the self-hosted forges.
func (c config) validateHosts(set map[string]jsontext.Value) error {
	hosts := []struct{ key, value string }{
		{"github_host", c.GitHubHost},
		{"gitlab_host", c.GitLabHost},
		{"azure_devops_host", c.AzureHost},
	}
	for _, k := range hosts {
		if set[k.key] != nil && !isHostname(k.value) {
			return invalidError(k.key, "a bare hostname", k.value)
		}
	}
	return nil
}

// validateKeybindings checks the key bindings. A list that is set must not
// be empty, and each key must have a valid name. Once the settings are
// applied to the default keys, no key may be bound to two actions.
func (c config) validateKeybindings() error {
	k := defaultKeys()
	bs := k.bindings(c.Keybindings)
	for _, b := range bs {
		if b.keys == nil {
			continue
		}
		want := "a list of keys, each a character or a named key (" + keyNames() +
			"), alone or after ctrl+ or alt+"
		if len(b.keys) == 0 {
			return invalidError("keybindings."+b.name, want, "[]")
		}
		for _, s := range b.keys {
			if _, ok := keyName(s); !ok {
				return invalidError("keybindings."+b.name, want, s)
			}
		}
	}
	k.applyConfig(c.Keybindings)
	first := map[string]string{} // the first action bound to each key
	for _, b := range bs {
		for _, key := range b.key.Keys() {
			switch a, ok := first[key]; {
			case ok && a == b.name:
				return fmt.Errorf("invalid value for keybindings: %q is bound twice to %q",
					settingKeyName(key), b.name)
			case ok:
				return fmt.Errorf("invalid value for keybindings: %q is bound to both %q and %q",
					settingKeyName(key), a, b.name)
			}
			first[key] = b.name
		}
	}
	return nil
}

// invalidError returns an error for an invalid value of the setting key.
// want describes the accepted values.
func invalidError(key, want, value string) error {
	return fmt.Errorf("invalid value for %s: expected %s, not %q", key, want, value)
}

// hasOnlyPlaceholders reports whether every { in command starts one of the
// known placeholders, such as {file}.
func hasOnlyPlaceholders(command string, known ...string) bool {
	for _, rest, ok := strings.Cut(command, "{"); ok; _, rest, ok = strings.Cut(rest, "{") {
		name, _, closed := strings.Cut(rest, "}")
		if !closed || !slices.Contains(known, name) {
			return false
		}
	}
	return true
}

// The maximum lengths of a DNS name and of one of its labels (RFC 1035).
const (
	maxHostname  = 253
	maxHostLabel = 63
)

// isHostname reports whether s is a bare DNS name, such as
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

// isHostLabel reports whether s is a valid label of a DNS name.
func isHostLabel(s string) bool {
	if s == "" || len(s) > maxHostLabel || s[0] == '-' || s[len(s)-1] == '-' {
		return false
	}
	return !strings.ContainsFunc(s, func(r rune) bool {
		letter := 'a' <= r && r <= 'z' || 'A' <= r && r <= 'Z'
		return !letter && !('0' <= r && r <= '9') && r != '-'
	})
}

// focusesAgent reports whether focus moves to the agent after sending a
// review.
func (c config) focusesAgent() bool { return c.FocusAgent == nil || *c.FocusAgent }

// validateChoice returns an error if value is not in allowed. key names
// the setting in the error message.
func validateChoice[T ~string](key string, value T, allowed []T) error {
	if slices.Contains(allowed, value) {
		return nil
	}
	want := make([]string, len(allowed))
	for i, a := range allowed {
		want[i] = string(a)
	}
	return invalidError(key, "one of "+strings.Join(want, ", "), string(value))
}
