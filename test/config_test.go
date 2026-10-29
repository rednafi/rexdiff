package test

import (
	"cmp"
	"encoding/json/v2"
	"regexp"
	"strings"
	"testing"
)

// actionNames lists every action that the keybindings setting accepts.
// alias is a former rexdiff name that the setting also accepts, or "".
var actionNames = []struct{ name, alias string }{
	{"down", ""},
	{"up", ""},
	{"next-hunk", ""},
	{"prev-hunk", ""},
	{"next-file", ""},
	{"prev-file", ""},
	{"collapse", ""},
	{"expand", ""},
	{"page-up", ""},
	{"page-down", ""},
	{"half-up", ""},
	{"half-down", ""},
	{"focus", ""},
	{"scope-uncommitted", ""},
	{"scope-branch", ""},
	{"scope-last-turn", ""},
	{"scope-commits", ""},
	{"base-pick", ""},
	{"commit-pick", ""},
	{"tab-changes", ""},
	{"tab-all-files", ""},
	{"tab-pr", ""},
	{"wrap", ""},
	{"rendered", "preview"},
	{"split", ""},
	{"navigator-position", ""},
	{"navigator-hide", ""},
	{"navigator-grow", "list-wider"},
	{"navigator-shrink", "list-narrower"},
	{"select", ""},
	{"comment", ""},
	{"edit", ""},
	{"delete", ""},
	{"next-comment", ""},
	{"prev-comment", ""},
	{"comments", ""},
	{"search", ""},
	{"find", ""},
	{"goto-line", ""},
	{"keys", ""},
	{"send", ""},
	{"copy", ""},
	{"open-pr", ""},
	{"refresh", ""},
	{"quit", ""},
	{"quit-discard", ""},
}

// actionKeys holds the keys that TestConfigNames binds, one for each
// action in actionNames, in order. It leaves out U, which is always the
// update key.
const actionKeys = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTVWXYZ"

// TestConfigNames checks that a config that uses the action names, or
// their aliases, binds every key and sets the position and width of the
// file list. It checks the key bindings through the help screen.
func TestConfigNames(t *testing.T) {
	keyOf := map[string]string{}
	for i, a := range actionNames {
		keyOf[a.name] = actionKeys[i : i+1]
	}
	// Each help entry lists the keys followed by a label. Some labels
	// cover two or three actions, such as "next/prev file".
	k := func(names ...string) string {
		keys := make([]string, len(names))
		for i, n := range names {
			keys[i] = keyOf[n]
		}
		return strings.Join(keys, "/")
	}
	help := [][2]string{
		{k("down", "up"), "move"},
		{k("half-down", "half-up"), "half page"},
		{k("next-hunk", "prev-hunk"), "next/prev change"},
		{k("next-file", "prev-file"), "next/prev file"},
		{k("focus"), "switch pane"},
		{k("collapse", "expand"), "scroll sideways"},
		{k("goto-line"), "go to line"},
		{k("tab-changes", "tab-all-files", "tab-pr"), "changes/files/PR"},
		{k("scope-uncommitted"), "uncommitted"},
		{k("scope-branch"), "branch"},
		{k("scope-last-turn"), "last turn"},
		{k("scope-commits"), "commits"},
		{k("base-pick"), "pick base branch"},
		{k("commit-pick"), "pick commits"},
		{k("select"), "select lines"},
		{k("comment"), "comment"},
		{k("edit"), "edit comment/open editor"},
		{k("delete"), "delete comment"},
		{k("next-comment", "prev-comment"), "next/prev comment"},
		{k("comments"), "list comments"},
		{k("send"), "send to agent"},
		{k("copy"), "copy comments"},
		{k("search"), "search files and code"},
		{k("find"), "find in file"},
		{k("rendered"), "rendered markdown"},
		{k("split"), "side by side"},
		{k("wrap"), "wrap lines"},
		{k("navigator-position"), "move list"},
		{k("navigator-hide"), "hide list"},
		{k("navigator-grow", "navigator-shrink"), "resize list"},
		{k("open-pr"), "open PR in browser"},
		{k("refresh"), "refresh"},
		{k("keys"), "all keys"},
		{k("quit"), "quit"},
	}
	var res []*regexp.Regexp
	for _, h := range help {
		res = append(res, regexp.MustCompile(`(^|\s)`+regexp.QuoteMeta(h[0])+` +`+regexp.QuoteMeta(h[1])))
	}
	for _, names := range []string{"names", "aliases"} {
		t.Run(names, func(t *testing.T) {
			kb := map[string][]string{}
			for _, a := range actionNames {
				name := a.name
				if names == "aliases" {
					name = cmp.Or(a.alias, a.name)
				}
				kb[name] = []string{keyOf[a.name]}
			}
			b, err := json.Marshal(map[string]any{
				"keybindings": kb, "file_list": "left", "file_list_width": 50, "url_opener": "opener {url}",
			})
			if err != nil {
				t.Fatal(err)
			}
			tm := start(t, newRepo(t), opts{width: 220, args: []string{"-config", string(b)}})
			s := tm.waitFor("Changes (2)")
			if c := column(s, "Changes (2)"); c < 0 || c > 5 {
				t.Errorf("the file list starts at column %d, want it at the left", c)
			}
			if ls := lines(s, "M a.go"); len(ls) == 0 || column(ls[0], "│") < 48 || column(ls[0], "│") > 52 {
				t.Errorf("the file list rows %q, want a width of 50", ls)
			}
			tm.keys(keyOf["keys"])
			tm.waitUntil(func(s string) bool {
				for _, re := range res {
					if !re.MatchString(s) {
						return false
					}
				}
				return true
			}, "show the keys of the config in the help")
		})
	}
}
