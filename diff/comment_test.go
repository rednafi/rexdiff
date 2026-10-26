package diff_test

import (
	"encoding/json/v2"
	"errors"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/rednafi/rexdiff/diff"
	"github.com/rednafi/rexdiff/internal/proc"
	"github.com/rednafi/rexdiff/internal/testgit"
)

// comment returns a comment with text on all rows of the diff of before
// and after.
func comment(path string, s diff.Side, before, after, text string) diff.Comment {
	rows := diff.Render(path, before, after, diff.Options{}).Rows
	c := diff.NewComment(path, s, rows, before, after)
	c.Text = text
	return c
}

// commentOn returns a comment on lines start to end of src, the file at
// path. The rows come from [diff.RenderFile].
func commentOn(path string, s diff.Side, src string, start, end int) diff.Comment {
	rows := diff.RenderFile(path, src, diff.Options{}).Rows
	return diff.NewComment(path, s, rows[start-1:end], src, src)
}

// review returns the text of [diff.Comments.Review] for c only.
func review(c diff.Comment) string {
	var cs diff.Comments
	cs.Add(c)
	return cs.Review("")
}

// TestReviewMatchesHerdrReviewr checks that Review writes the text format
// of herdr-reviewr.
func TestReviewMatchesHerdrReviewr(t *testing.T) {
	t.Parallel()
	var cs diff.Comments
	cs.Add(comment("c.go", diff.SideNew,
		"ctx\n\told\x1b[31m\n",
		"ctx\n\tnew\n",
		"fix\x1b]7501;state=idle\x07 this",
	))
	cs.Add(comment("b.go", diff.SideOld, "gone()\n", "", "why?"))
	cs.Add(comment("a.go", diff.SideNew,
		"",
		"x := 1\ny := 2\n",
		"merge these\n\n  \nplease  ",
	))

	got := cs.Review("Fix each comment.\r\n")
	want := "Fix each comment.\n\n" +
		"a.go:1-2\n+x := 1\n+y := 2\nmerge these\nplease\n\n" +
		"b.go:1 (removed)\n-gone()\nwhy?\n\n" +
		"c.go:1-2\n" +
		" ctx\n-\told[31m\n+\tnew\nfix]7501;state=idle this"
	if got != want {
		t.Errorf("Review = \n%s\nwant:\n%s", got, want)
	}
	if got := new(diff.Comments).Review(""); got != "" {
		t.Errorf("Review with no comments = %q, want \"\"", got)
	}
}

// TestReviewContinuedRows checks a comment on only continued rows.
func TestReviewContinuedRows(t *testing.T) {
	t.Parallel()
	before, after := "", strings.Repeat("word ", 20)+"\n"
	d := diff.Render("a.go", before, after, diff.Options{Width: 30, Wrap: true})
	var cont []diff.Row
	for _, r := range d.Rows {
		if r.Continued {
			cont = append(cont, r)
		}
	}
	c := diff.NewComment("a.go", diff.SideNew, cont, before, after)
	c.Text = "long"
	// The comment has no diff lines, so Review writes the lines of its range.
	want := "a.go:1\n+" + strings.TrimSuffix(after, "\n") + "\nlong"
	if got := review(c); len(cont) == 0 || got != want {
		t.Errorf("%d continued rows, Review = \n%s\nwant:\n%s",
			len(cont), got, want)
	}
}

// TestComments checks Add, Find, At, EndingAt, Count, and Remove.
func TestComments(t *testing.T) {
	t.Parallel()
	var cs diff.Comments
	d := diff.Anchor{InDiff: true}
	cs.Add(diff.Comment{Path: "a.go", Side: diff.SideNew, Start: 2, End: 4, Anchor: d})
	cs.Add(diff.Comment{Path: "a.go", Side: diff.SideOld, Start: 4, End: 4, Anchor: d})
	cs.Add(diff.Comment{Path: "b.go", Side: diff.SideNew, Start: 1, End: 1, Anchor: d})
	a, b := cs.All()[0], cs.All()[1]
	if cs.Len() != 3 || cs.Count(d, "a.go") != 2 || a.ID == b.ID {
		t.Fatalf("Len = %d, Count(a.go) = %d, IDs %d %d; "+
			"want 3, 2, two different IDs",
			cs.Len(), cs.Count(d, "a.go"), a.ID, b.ID)
	}
	if c := cs.At(d, "a.go", diff.SideNew, 3); c != a {
		t.Errorf("At(a.go, New, 3) = %v, want %v", c, a)
	}
	if c := cs.At(d, "a.go", diff.SideNew, 5); c != nil {
		t.Errorf("At(a.go, New, 5) = %v, want nil", c)
	}
	if got := cs.EndingAt(d, "a.go", diff.SideOld, 4); len(got) != 1 || got[0] != b {
		t.Errorf("EndingAt(a.go, Old, 4) = %v, want [%v]", got, b)
	}
	// A comment shows only in its kind of view, and at its revision.
	for _, other := range []diff.Anchor{{}, {InDiff: true, Rev: "a..b"}} {
		if c := cs.At(other, "a.go", diff.SideNew, 3); c != nil {
			t.Errorf("At(%+v, a.go, New, 3) = %v, want nil", other, c)
		}
		if got := cs.EndingAt(other, "a.go", diff.SideOld, 4); got != nil {
			t.Errorf("EndingAt(%+v, a.go, Old, 4) = %v, want nil", other, got)
		}
		if n := cs.Count(other, "a.go"); n != 0 {
			t.Errorf("Count(%+v, a.go) = %d, want 0", other, n)
		}
	}
	if cs.Find(b.ID) != b {
		t.Errorf("Find(%d) = %v, want %v", b.ID, cs.Find(b.ID), b)
	}
	cs.Remove(b.ID)
	if cs.Find(b.ID) != nil || cs.Len() != 2 {
		t.Errorf("after Remove(%d): Find = %v, Len = %d, want nil, 2",
			b.ID, cs.Find(b.ID), cs.Len())
	}
	cs.Add(diff.Comment{Path: "c.go"})
	if c := cs.All()[2]; c.ID == a.ID || c.ID == b.ID {
		t.Errorf("Add gives ID %d, want an ID other than %d and %d",
			c.ID, a.ID, b.ID)
	}
}

// TestNewComment checks comments from the rows of a change, in the two
// layouts, on each side.
func TestNewComment(t *testing.T) {
	t.Parallel()
	before := "a\nb\nc\nd\n"
	after := "a\nB\nC\nd\n"
	for _, split := range []bool{false, true} {
		rows := diff.Render("x.txt", before, after, diff.Options{Split: split}).Rows
		// The rows from "a" to "d".
		lo := slices.IndexFunc(rows, isEqual)
		for _, s := range []diff.Side{diff.SideNew, diff.SideOld} {
			c := diff.NewComment("x.txt", s, rows[lo:], before, after)
			if c.Path != "x.txt" || c.Side != s || c.Start != 1 || c.End != 4 {
				t.Errorf("split %v, side %d: %+v, want lines 1-4", split, s, c)
			}
			want := "x.txt:1-4"
			if s == diff.SideOld {
				want += " (removed)"
			}
			want += "\n a\n-b\n-c\n+B\n+C\n d\n"
			if got := review(c); got != want {
				t.Errorf("split %v, side %d: Review = \n%s\nwant:\n%s",
					split, s, got, want)
			}
		}
		// A comment on only the inserted lines has its range on the new side.
		var ins []diff.Row
		for _, r := range rows {
			if r.Kind == diff.KindInsert || r.Kind == diff.KindChange {
				ins = append(ins, r)
			}
		}
		got := diff.NewComment("x.txt", diff.SideNew, ins, before, after)
		if got.Start != 2 || got.End != 3 {
			t.Errorf("split %v, the new lines: %+v, want lines 2-3", split, got)
		}
	}
	// A hunk header has no line.
	rows := diff.Render("x.txt", before, after, diff.Options{}).Rows
	hunk := rows[:slices.IndexFunc(rows, isEqual)]
	got := diff.NewComment("x.txt", diff.SideNew, hunk, before, after)
	if len(hunk) == 0 || got.Start != 0 || got.End != 0 {
		t.Errorf("a hunk header %v: %+v, want no lines", hunk, got)
	}
}

// TestSnippetIsTheRealDiff checks that the snippet matches git diff, with raw
// text and tabs, in both layouts.
func TestSnippetIsTheRealDiff(t *testing.T) {
	t.Parallel()
	before := "func a() {\n\treturn 1\n}\n"
	after := "func a() {\n\t// two\n\treturn 2\n}\n"
	want := " func a() {\n-\treturn 1\n+\t// two\n+\treturn 2\n }"
	for _, split := range []bool{false, true} {
		d := diff.Render("x.go", before, after, diff.Options{Split: split})
		got := strings.Join(snippet(d.Rows, before, after), "\n")
		if got != want {
			t.Errorf("split %v: snippet = \n%s\nwant:\n%s", split, got, want)
		}
	}

	dir := t.TempDir()
	for name, src := range map[string]string{"old": before, "new": after} {
		testgit.WriteFile(t, dir, name, src)
	}
	// git diff exits with 1 when the files differ.
	// No diff setting of the user applies.
	git := proc.Command(t.Context(), testgit.ToolEnv(t),
		"git", "diff", "--no-index", "-U1",
		filepath.Join(dir, "old"),
		filepath.Join(dir, "new"),
	)
	out, err := git.Output()
	if ee, ok := errors.AsType[*exec.ExitError](err); err != nil && (!ok || ee.ExitCode() != 1) {
		t.Fatalf("git diff --no-index: %v", err)
	}
	_, body, _ := strings.Cut(string(out), "@@\n")
	if strings.TrimSuffix(body, "\n") != want {
		t.Errorf("git diff = \n%s\nwant:\n%s", body, want)
	}
}

// TestCommentsJSON checks that the JSON keeps the anchor of a comment. It
// also checks a file of an older rexdiff, which has a stale field and no
// anchor.
func TestCommentsJSON(t *testing.T) {
	t.Parallel()
	var cs diff.Comments
	anchors := []diff.Anchor{{}, {InDiff: true}, {InDiff: true, Rev: "a..b"}}
	for _, a := range anchors {
		cs.Add(diff.Comment{Path: "a.go", Start: 1, End: 1, Anchor: a})
	}
	b, err := cs.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	var back diff.Comments
	if err := back.UnmarshalJSON(b); err != nil {
		t.Fatal(err)
	}
	for i, c := range back.All() {
		if c.Anchor != anchors[i] {
			t.Errorf("comment %d: anchor %+v, want %+v", i, c.Anchor, anchors[i])
		}
	}

	old := `{"last":1,"items":[{"id":1,"path":"a.go","side":1,"start":2,` +
		`"end":3,"text":"x","stale":true,"code":["a","b"],"snippet":null}]}`
	var cs2 diff.Comments
	if err := cs2.UnmarshalJSON([]byte(old)); err != nil {
		t.Fatalf("UnmarshalJSON of an old file: %v", err)
	}
	c := cs2.Find(1)
	if c == nil {
		t.Fatal("Find(1) = nil, want the comment of an old file")
	}
	if c.Anchor != (diff.Anchor{InDiff: true}) || c.Start != 2 {
		t.Errorf("comment of an old file = %+v, want lines 2-3 on a diff", c)
	}
	back.SetOldRev("x..y")
	cs2.SetOldRev("a..b")
	for i, c := range back.All() {
		if c.Anchor != anchors[i] {
			t.Errorf("comment %d after SetOldRev: anchor %+v, want %+v", i, c.Anchor, anchors[i])
		}
	}
	if want := (diff.Anchor{InDiff: true, Rev: "a..b"}); c.Anchor != want {
		t.Errorf("comment of an old file after SetOldRev: anchor %+v, want %+v", c.Anchor, want)
	}
}

// TestCommentsJSONLatin1 checks that a comment on a file that is not UTF-8,
// for example a Latin-1 file, survives a JSON round trip. The bytes that
// are not UTF-8 become U+FFFD.
func TestCommentsJSONLatin1(t *testing.T) {
	t.Parallel()
	const path = "caf\xe9.txt"
	var cs diff.Comments
	c := commentOn(path, diff.SideNew, "caf\xe9 cr\xe8me\n", 1, 1)
	c.Text = "na\xefve"
	cs.Add(c)
	b, err := json.Marshal(&cs)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var back diff.Comments
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	got := back.Find(1)
	if got == nil {
		t.Fatal("Find(1) = nil after a round trip")
	}
	want := "caf\uFFFD.txt:1\n caf\uFFFD cr\uFFFDme\nna\uFFFDve"
	if r := review(*got); r != want {
		t.Errorf("Review after a round trip = %q, want %q", r, want)
	}
}
