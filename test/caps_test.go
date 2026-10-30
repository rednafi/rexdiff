package test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rednafi/rexdiff/internal/testgit"
)

func markdownRepo(t *testing.T, text string) string {
	t.Helper()
	repo := newRepo(t)
	testgit.Git(t, repo, "checkout", "--", "a.go", "long.txt")
	testgit.WriteFile(t, repo, "README.md", text)
	return repo
}

var rendered = []string{"-config", `{"markdown_view": "rendered"}`}

func TestMarkdownTooLarge(t *testing.T) {
	text := "# Big title\n\n" + strings.Repeat("Some *text* in a paragraph.\n\n", 20000) // about 600 KB
	tm := start(t, markdownRepo(t, text), opts{args: rendered})
	tm.waitFor("README.md", "markdown too large to render", "# Big title", "Some *text*")
}

// TestMarkdownBlockTooLarge checks that reference links still resolve in
// the blocks that render.
func TestMarkdownBlockTooLarge(t *testing.T) {
	table := "| a | b |\n|---|---|\n" + strings.Repeat("| *x* | y |\n", 7000) // about 80 KB
	text := "# Title\n\nSee [the docs][Docs].\n\n[docs]: https://example.com/docs\n\n" + table
	tm := start(t, markdownRepo(t, text), opts{args: rendered})
	s := tm.waitFor("See the docs", "https://example.com/docs", "block too large to render", "| *x* | y |")
	if strings.Contains(s, "# Title") || strings.Contains(s, "[docs]:") {
		t.Errorf("the small blocks show as source. The screen shows:\n%s", s)
	}
}

// TestMarkdownTooDeep guards against glamour freezing on deeply nested
// blocks.
func TestMarkdownTooDeep(t *testing.T) {
	text := "# Title\n\n" + strings.Repeat("> ", 100) + "deep quote\n\n" +
		strings.Repeat("- ", 300) + "deep list\n\nAfter the *lists*.\n"
	for _, width := range []uint16{40, 120, 300} {
		t.Run(fmt.Sprintf("%d columns", width), func(t *testing.T) {
			tm := start(t, markdownRepo(t, text), opts{args: rendered, width: width})
			s := tm.waitFor("Title", "nested too deeply", "> > >")
			if strings.Contains(s, "# Title") {
				t.Errorf("the heading shows as source. The screen shows:\n%s", s)
			}
			tm.keys(":", "$", "enter")
			tm.waitFor("After the lists.", "- - -")
		})
	}
}

func nestedList(marker string, depth int) string {
	var b strings.Builder
	for d := range depth {
		fmt.Fprintf(&b, "%s%s item %d\n", strings.Repeat(" ", d*(len(marker)+1)), marker, d)
	}
	return b.String()
}

// TestMarkdownDepth checks the nesting limit. Blocks up to 12 deep render.
func TestMarkdownDepth(t *testing.T) {
	const note = "nested too deeply"
	fence := "```\n" + strings.Repeat(" ", 26) + "deep code 26\n" +
		strings.Repeat(" ", 40) + "deep code 40\n```\n"
	for _, c := range []struct {
		name  string
		block string
		deep  bool
		whole bool // the block takes in the whole file
		shows []string
		hides []string
	}{
		{name: "ordered list 9 deep", block: nestedList("1.", 9), shows: []string{"item 8"}},
		{name: "ordered list 12 deep", block: nestedList("1.", 12), shows: []string{"item 11"}},
		{name: "bullet list 12 deep", block: nestedList("-", 12), shows: []string{"item 11"}, hides: []string{"- item"}},
		{name: "quote 12 deep", block: strings.Repeat("> ", 12) + "deep quote\n", shows: []string{"deep quote"}, hides: []string{"> >"}},
		{name: "indented fenced code", block: fence, shows: []string{"deep code 26", "deep code 40"}, hides: []string{"```"}},
		{name: "fenced code in a list", block: "- item\n\n  " + strings.ReplaceAll(fence, "\n", "\n  "), shows: []string{"deep code 40"}, hides: []string{"```"}},
		{name: "indented code", block: "    code\n" + strings.Repeat(" ", 34) + "deep code 30\n", shows: []string{"deep code 30"}},
		// Within a quote, a deep quote is code inside the fence.
		{name: "quote in a fence in a quote", block: "> ```\n" + strings.Repeat("> ", 50) + "x\n", shows: []string{"> > >"}},

		{name: "quote 13 deep", block: strings.Repeat("> ", 13) + "deep\n", deep: true, shows: []string{"> > >"}},
		{name: "quote 100 deep", block: strings.Repeat("> ", 100) + "deep\n", deep: true, shows: []string{"> > >"}},
		{name: "list 13 deep", block: strings.Repeat("- ", 13) + "deep\n", deep: true, shows: []string{"- - -"}},
		{name: "list 300 deep", block: strings.Repeat("- ", 300) + "deep\n", deep: true, shows: []string{"- - -"}},
		{name: "ordered list 13 deep", block: nestedList("1.", 13), deep: true, shows: []string{"1. item 12"}},
		{name: "mixed", block: strings.Repeat("> - 1. ", 30) + "deep\n", deep: true, shows: []string{"> - 1. > - 1."}},
		// A list item ends the code block of the item above.
		{name: "list after an unclosed fence", block: "- ```\n" + nestedList("-", 13), deep: true, shows: []string{"- item 12"}},
		// An indented fence is code. It joins the heading's block and takes in
		// the rest of the file.
		{name: "indented code is not a fence", block: "    ```\n" + strings.Repeat("- ", 20) + "x\n", deep: true, whole: true, shows: []string{"- - -"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			text := "# Title\n\n" + c.block + "\nAfter the *end*.\n"
			tm := start(t, markdownRepo(t, text), opts{args: rendered})
			after := "After the end."
			if c.whole {
				after = "After the *end*."
			}
			s := tm.waitFor(append([]string{"Title", after}, c.shows...)...)
			if !c.whole && strings.Contains(s, "# Title") {
				t.Errorf("the heading shows as source. The screen shows:\n%s", s)
			}
			if got := strings.Contains(s, note); got != c.deep {
				t.Errorf("the screen shows %q: %v, want %v. The screen shows:\n%s", note, got, c.deep, s)
			}
			if hasAny(s, c.hides...) {
				t.Errorf("the screen shows one of %q. It shows:\n%s", c.hides, s)
			}
		})
	}
}

// TestMarkdownLinkDefsTooLarge guards against a freeze from many
// references to link definitions.
func TestMarkdownLinkDefsTooLarge(t *testing.T) {
	t.Run("many references", func(t *testing.T) {
		var b strings.Builder
		b.WriteString("# Refs\n\n")
		for i := range 3000 {
			fmt.Fprintf(&b, "[d%d]\n\n", i%3)
		}
		for i := range 3 {
			fmt.Fprintf(&b, "[d%d]: https://example.com/%s\n", i, strings.Repeat("a", 1900))
		}
		tm := start(t, markdownRepo(t, b.String()), opts{args: rendered})
		tm.waitFor("README.md", "markdown too large to render", "# Refs")
	})
	t.Run("long definition", func(t *testing.T) {
		text := "# Refs\n\n" + strings.Repeat("See [d].\n\n", 1000) +
			"[d]: https://example.com/" + strings.Repeat("a", 60000) + "\n"
		tm := start(t, markdownRepo(t, text), opts{args: rendered})
		s := tm.waitFor("Refs", "See [d].")
		if strings.Contains(s, "# Refs") || strings.Contains(s, "aaaa") {
			t.Errorf("the file shows as source or with the long link. The screen shows:\n%s", s)
		}
	})
}

func prRepo(t *testing.T, body string) (repo, bin string) {
	t.Helper()
	repo = newRepo(t)
	testgit.Git(t, repo, "remote", "add", "origin", "https://github.com/o/r.git")
	data, err := json.Marshal(map[string]any{
		"number": 7, "title": "Hostile PR", "state": "OPEN", "url": "https://github.com/o/r/pull/7",
		"body": body, "author": map[string]string{"login": "ann"},
		"headRefName": "feat", "baseRefName": "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	bin = tools(t, map[string]string{"gh": `cat "${0%/*}/pr.json"` + "\n"})
	if err := os.WriteFile(filepath.Join(bin, "pr.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return repo, bin
}

func TestPRMarkdownTooHard(t *testing.T) {
	for name, c := range map[string]struct{ body, note string }{
		"deep quote": {strings.Repeat("> ", 100) + "deep", "text nested too deeply to render"},
		"deep list":  {strings.Repeat("- ", 300) + "deep", "text nested too deeply to render"},
		"references": {
			strings.Repeat("[d] ", 2000) + "\n\n[d]: https://example.com/" + strings.Repeat("a", 5000),
			"text too large to render",
		},
	} {
		t.Run(name, func(t *testing.T) {
			repo, bin := prRepo(t, c.body)
			tm := start(t, repo, opts{path: bin})
			tm.keys("3")
			tm.waitFor("Hostile PR #7", c.note)
		})
	}
}

// TestPRTooLarge checks that thousands of <details> tags do not stop the
// PR tab from showing.
func TestPRTooLarge(t *testing.T) {
	repo := newRepo(t)
	testgit.Git(t, repo, "remote", "add", "origin", "https://github.com/o/r.git")
	hostile := strings.Repeat("<details><summary>", 3600) // 65 KB
	var comments []map[string]any
	for i := range 20 {
		comments = append(comments, map[string]any{
			"author": map[string]string{"login": fmt.Sprint("user", i)}, "body": hostile,
			"createdAt": "2024-01-01T00:00:00Z",
		})
	}
	data, err := json.Marshal(map[string]any{
		"number": 7, "title": "Big PR", "state": "OPEN", "url": "https://github.com/o/r/pull/7",
		"body": "The body.", "author": map[string]string{"login": "ann"},
		"headRefName": "feat", "baseRefName": "main", "comments": comments,
	})
	if err != nil {
		t.Fatal(err)
	}
	bin := tools(t, map[string]string{"gh": `cat "${0%/*}/pr.json"` + "\n"})
	if err := os.WriteFile(filepath.Join(bin, "pr.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	tm := start(t, repo, opts{path: bin})
	tm.keys("3")
	s := tm.waitFor("Big PR #7", "The body.", "user0", "… truncated · o to open in browser")
	if strings.Contains(s, "<details>") {
		t.Errorf("the comments show their tags. The screen shows:\n%s", s)
	}
}

func TestTallLine(t *testing.T) {
	repo := newRepo(t)
	testgit.Git(t, repo, "checkout", "--", "long.txt")
	var b strings.Builder
	b.WriteString("package a\n\n// START")
	for i := range 4000 {
		fmt.Fprintf(&b, " w%d", i)
	}
	b.WriteString(" THE-END\nvar next = 1\n")
	testgit.WriteFile(t, repo, "a.go", b.String())
	log := filepath.Join(t.TempDir(), "log")
	tm := start(t, repo, opts{args: []string{"-log", log}})
	t.Cleanup(func() {
		if t.Failed() {
			b, _ := os.ReadFile(log)
			for _, l := range strings.Split(string(b), "\n") {
				if strings.Contains(l, "msg=key") {
					t.Log(l)
				}
			}
			_ = time.Now
		}
	})
	// A partly drawn frame has no footer or a gap in the words. Or its last
	// line is blank because the terminal scrolled the old lines up.
	whole := func(s string) bool {
		last, _, _ := strings.Cut(lastBodyLine(s), "│")
		return strings.Contains(s, "q quit") && wordsInOrder(s) && strings.TrimSpace(last) != ""
	}
	// The file list gets wider when the line counts arrive, which wraps
	// the line again, so wait for them first.
	s := tm.waitUntil(func(s string) bool {
		return strings.Contains(s, "// START") && sameLine(s, "M a.go", "+2 -3") && whole(s)
	}, "show the start of the line and its counts")
	for i := 1; !strings.Contains(s, "THE-END"); i++ {
		if i == 40 {
			t.Fatalf("40 half pages do not reach the end of the line. The screen shows:\n%s", s)
		}
		tm.keys("ctrl+d")
		waitFile(t, log, fmt.Sprintf("%d keys", i), func(l string) bool { return strings.Count(l, "msg=key") == i })
		prev := s
		s = tm.waitUntil(func(s string) bool { return s != prev && whole(s) }, "move the view")
	}
	if strings.Contains(s, "// START") || strings.Contains(s, "var next") {
		t.Errorf("the screen shows more than the end of the line:\n%s", s)
	}
	tm.keys("pgup")
	tm.waitGone("THE-END")
	tm.keys("pgdown")
	tm.waitFor("THE-END")
	tm.keys("j")
	tm.waitCursor("var next")
}

func lastBodyLine(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n "), "\n")
	if len(lines) < 2 {
		return ""
	}
	return lines[len(lines)-2]
}

func wordsInOrder(s string) bool {
	words := regexp.MustCompile(`\bw(\d+)\b`).FindAllStringSubmatch(s, -1)
	for i := 1; i < len(words); i++ {
		prev, _ := strconv.Atoi(words[i-1][1])
		n, _ := strconv.Atoi(words[i][1])
		if n != prev+1 {
			return false
		}
	}
	return len(words) > 0
}
