package test

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/rednafi/rexdiff/internal/testgit"
)

var manyFiles = testgit.NewFixture(func(tb testing.TB, dir string) {
	tb.Helper()
	testgit.InitRepo(tb, dir)
	for i := range navigatorFiles {
		testgit.WriteFile(tb, dir, fileName(i), numbered("old", 1, 3))
	}
	testgit.Git(tb, dir, "add", ".")
	testgit.Git(tb, dir, "commit", "-qm", "first")
	for i := range navigatorFiles {
		testgit.WriteFile(tb, dir, fileName(i), content(i)+"\n"+numbered("old", 1, 3))
	}
})

const navigatorFiles = 60

func fileName(i int) string { return fmt.Sprintf("f%02d.txt", i) }

func content(i int) string { return fmt.Sprintf("content of file %02d", i) }

func navigatorRow(i int) string { return "M " + fileName(i) }

func newManyFiles(t *testing.T) string {
	t.Helper()
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(tmp, "repo")
	manyFiles.Copy(t, dir)
	return dir
}

func rowY(s, text string) int {
	if _, y, ok := position(s, text); ok {
		return y
	}
	return -1
}

func shownFiles(s string) (first, last int) {
	first, last = -1, -1
	for i := range navigatorFiles {
		if rowY(s, navigatorRow(i)) >= 0 {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	return first, last
}

// TestNavigatorWheelClick checks that a click after a wheel scroll does
// not scroll the list again.
func TestNavigatorWheelClick(t *testing.T) {
	tm := start(t, newManyFiles(t), opts{height: 20})
	tm.waitFor(navigatorRow(0), content(0))
	// Each wheel step scrolls three rows.
	tm.wheel(navigatorRow(0), 10)
	s := tm.waitUntil(func(s string) bool {
		first, _ := shownFiles(s)
		return first == 30
	}, "scroll the list to file 30")
	if strings.Contains(s, navigatorRow(0)) || !strings.Contains(s, content(0)) {
		t.Fatalf("the wheel does not scroll the list only:\n%s", s)
	}
	first, last := shownFiles(s)
	y := rowY(s, navigatorRow(40))
	tm.click(navigatorRow(40), 1)
	// A frame can be drawn in parts, so each wait checks the whole screen.
	tm.waitUntil(func(s string) bool {
		return strings.Contains(s, content(40)) && rowY(s, navigatorRow(40)) == y
	}, fmt.Sprintf("open file 40 with its row on line %d", y))

	// The list scrolls by one row only past the top or bottom row.
	for _, step := range []struct {
		key         string
		from, to    int
		first, last int // files shown afterwards
	}{
		{key: "k", from: 40, to: first, first: first, last: last},
		{key: "k", from: first, to: first - 1, first: first - 1, last: last - 1},
		{key: "j", from: first - 1, to: last - 1, first: first - 1, last: last - 1},
		{key: "j", from: last - 1, to: last, first: first, last: last},
	} {
		for i := step.from; i != step.to; {
			if step.key == "j" {
				i++
			} else {
				i--
			}
			tm.keys(step.key)
		}
		tm.waitUntil(func(s string) bool {
			f, l := shownFiles(s)
			return strings.Contains(s, content(step.to)) && f == step.first && l == step.last
		}, fmt.Sprintf("open file %d with files %d to %d in the list", step.to, step.first, step.last))
	}
	tm.keys("z")
	tm.waitGone(navigatorRow(first))
	tm.keys("z")
	tm.waitUntil(func(s string) bool {
		f, l := shownFiles(s)
		return f == first && l == last
	}, fmt.Sprintf("show files %d to %d in the list", first, last))
}

func TestMainWheelClick(t *testing.T) {
	tm := start(t, newRepo(t), opts{height: 20})
	tm.click("M long.txt", 1)
	tm.waitFor("⋯ 32 unmodified lines")
	tm.keys(":", "2", "0", "enter") // going to line 20 expands the fold
	tm.waitCursor("line 20")
	tm.keys(":", "1", "enter")
	tm.waitFor(" first")
	// Each wheel step scrolls three rows.
	tm.wheel(" first", 5)
	s := tm.waitUntil(func(s string) bool {
		return strings.Contains(s, "line 16") && !strings.Contains(s, "line 15")
	}, "scroll to line 16")
	y := rowY(s, "line 25")
	tm.click("line 25", 1)
	tm.waitUntil(func(s string) bool {
		return sameLine(s, "▶", "line 25") && rowY(s, "line 25") == y
	}, fmt.Sprintf("put the cursor on line 25, on line %d", y))
}

func TestWideNameCut(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	testgit.InitRepo(t, repo)
	names := []string{strings.Repeat("中文", 10) + ".txt", strings.Repeat("😀", 20) + ".md"}
	for _, n := range names {
		testgit.WriteFile(t, repo, n, "old\n")
	}
	testgit.Git(t, repo, "add", ".")
	testgit.Git(t, repo, "commit", "-qm", "first")
	for _, n := range names {
		testgit.WriteFile(t, repo, n, "new\n")
	}
	for _, w := range []int{30, 31} {
		t.Run(strconv.Itoa(w), func(t *testing.T) {
			tm := start(t, repo, opts{width: 90, height: 14, args: []string{
				"-config", fmt.Sprintf(`{"file_list_width": %d}`, w),
			}})
			tm.waitUntil(func(s string) bool {
				return sameLine(s, "M …", "文.txt", "+1 -1") && sameLine(s, "M …😀", "😀.md", "+1 -1")
			}, "the cut names with their whole line counts")
		})
	}
}

// TestWideTitleCut uses both parities to cut between and inside wide
// characters. A title too wide pushes the file list off the screen.
func TestWideTitleCut(t *testing.T) {
	for _, name := range []string{strings.Repeat("中文", 30) + ".txt", strings.Repeat("😀", 60) + ".md"} {
		repo := filepath.Join(t.TempDir(), "repo")
		testgit.InitRepo(t, repo)
		testgit.WriteFile(t, repo, name, "old\n")
		testgit.Git(t, repo, "add", ".")
		testgit.Git(t, repo, "commit", "-qm", "first")
		testgit.WriteFile(t, repo, name, "new\n")
		end := string([]rune(name)[len([]rune(name))-5:])
		for _, width := range []uint16{80, 81} {
			t.Run(end+"/"+strconv.Itoa(int(width)), func(t *testing.T) {
				tm := start(t, repo, opts{width: width, height: 14})
				s := tm.waitUntil(func(s string) bool {
					return sameLine(s, "…", end+"  +1 -1") && sameLine(s, "Changes (1)")
				}, "the cut title with its line count")
				if !sameLine(s, "M …", "+1 -1") {
					t.Errorf("the file list row is cut short:\n%s", s)
				}
			})
		}
	}
}
