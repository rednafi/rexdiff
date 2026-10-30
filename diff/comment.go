package diff

import (
	"cmp"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"slices"
	"strings"

	"github.com/rednafi/rexdiff/internal/termtext"
)

// Comment is a review note on a line range of one side of a file. It keeps
// its line numbers when the file changes.
type Comment struct {
	ID   int // [Comments.Add] sets it
	Path string
	Side Side
	// Start and End are 1-based and inclusive.
	Start, End int
	Text       string
	Anchor     Anchor

	code     []string
	snippet  []string // empty means use code
	noAnchor bool
}

// Anchor identifies the only view that shows a comment.
type Anchor struct {
	// InDiff is false for a comment in the file view.
	InDiff bool
	// Rev names the compared commits. "" means the working tree.
	Rev string
}

// NewComment returns an empty comment on rows of a [Diff]. Start and End
// are 0 if the rows have no line on side.
func NewComment(path string, side Side, rows []Row, oldText, newText string) Comment {
	c := Comment{Path: path, Side: side, snippet: snippet(rows, oldText, newText)}
	for _, r := range rows {
		n := r.Line(side)
		if n == 0 {
			continue
		}
		if c.Start == 0 || n < c.Start {
			c.Start = n
		}
		c.End = max(c.End, n)
	}
	if c.Start == 0 {
		return c
	}
	lines := SplitLines(versionText(side, oldText, newText))
	c.code = slices.Clone(lines[min(c.Start-1, len(lines)):min(c.End, len(lines))])
	return c
}

// Location returns a quoted path and line range, such as "a.go:3-4". It
// appends " (removed)" on the old side.
func (c *Comment) Location() string {
	loc := fmt.Sprintf("%s:%d", termtext.Quote(c.Path), c.Start)
	if c.End != c.Start {
		loc += fmt.Sprintf("-%d", c.End)
	}
	if c.Side == SideOld {
		loc += " (removed)"
	}
	return loc
}

// Moved reports whether text no longer holds the code of c on lines Start
// to End. It reports false if c has no code, as in older files.
func (c *Comment) Moved(text string) bool {
	if c.Start == 0 || len(c.code) != c.End-c.Start+1 {
		return false
	}
	n := 0
	for l := range strings.Lines(text) {
		n++
		if n > c.End {
			break
		}
		if n >= c.Start && trimEOL(l) != c.code[n-c.Start] {
			return true
		}
	}
	return n < c.End
}

// Comments is a set of review comments. The zero value is ready to use.
type Comments struct {
	last  int
	items []*Comment
}

// Len returns the number of comments.
func (cs *Comments) Len() int { return len(cs.items) }

// Add adds c and gives it a new ID.
func (cs *Comments) Add(c Comment) {
	cs.last++
	c.ID = cs.last
	cs.items = append(cs.items, &c)
}

// Delete removes the comment with the given ID, if there is one.
func (cs *Comments) Delete(id int) {
	cs.items = slices.DeleteFunc(cs.items, func(c *Comment) bool { return c.ID == id })
}

// All returns the comments sorted by path and start line.
func (cs *Comments) All() []*Comment {
	return slices.SortedStableFunc(slices.Values(cs.items), func(a, b *Comment) int {
		return cmp.Or(strings.Compare(a.Path, b.Path), cmp.Compare(a.Start, b.Start))
	})
}

// SetRev sets the Rev of comments read from a file without anchors.
func (cs *Comments) SetRev(rev string) {
	for _, c := range cs.items {
		if c.noAnchor {
			c.Anchor.Rev, c.noAnchor = rev, false
		}
	}
}

// Find returns the comment with the given ID, or nil.
func (cs *Comments) Find(id int) *Comment {
	if i := slices.IndexFunc(cs.items, func(c *Comment) bool { return c.ID == id }); i >= 0 {
		return cs.items[i]
	}
	return nil
}

// At returns the first comment with anchor a that covers line, or nil.
func (cs *Comments) At(a Anchor, path string, side Side, line int) *Comment {
	i := slices.IndexFunc(cs.items, func(c *Comment) bool {
		return c.Anchor == a && c.Path == path && c.Side == side &&
			line >= c.Start && line <= c.End
	})
	if i < 0 {
		return nil
	}
	return cs.items[i]
}

// EndingAt returns the comments with anchor a whose range ends on line.
func (cs *Comments) EndingAt(a Anchor, path string, side Side, line int) []*Comment {
	var ending []*Comment
	for _, c := range cs.items {
		if c.Anchor == a && c.Path == path && c.Side == side && c.End == line {
			ending = append(ending, c)
		}
	}
	return ending
}

// Count returns the number of comments with anchor a on the file at path.
func (cs *Comments) Count(a Anchor, path string) int {
	n := 0
	for _, c := range cs.items {
		if c.Anchor == a && c.Path == path {
			n++
		}
	}
	return n
}

func versionText(side Side, oldText, newText string) string {
	if side == SideOld {
		return oldText
	}
	return newText
}

type savedComment struct {
	ID    int    `json:"id"`
	Path  string `json:"path"`
	Side  Side   `json:"side"`
	Start int    `json:"start"`
	End   int    `json:"end"`
	Text  string `json:"text"`
	// InDiff is nil in older files. Such a comment is on a diff.
	InDiff  *bool    `json:"in_diff"`
	Rev     string   `json:"rev"`
	Code    []string `json:"code"`
	Snippet []string `json:"snippet"`
}

type savedComments struct {
	Last  int            `json:"last"`
	Items []savedComment `json:"items"`
}

// MarshalJSON replaces invalid UTF-8 with U+FFFD.
func (cs *Comments) MarshalJSON() ([]byte, error) {
	saved := savedComments{Last: cs.last}
	for _, c := range cs.items {
		saved.Items = append(saved.Items, savedComment{
			ID: c.ID, Path: c.Path, Side: c.Side, Start: c.Start, End: c.End,
			Text: c.Text, InDiff: &c.Anchor.InDiff, Rev: c.Anchor.Rev,
			Code: c.code, Snippet: c.snippet,
		})
	}
	return json.Marshal(saved, jsontext.AllowInvalidUTF8(true))
}

// UnmarshalJSON decodes comments encoded by [Comments.MarshalJSON].
func (cs *Comments) UnmarshalJSON(b []byte) error {
	var saved savedComments
	if err := json.Unmarshal(b, &saved); err != nil {
		return err
	}
	*cs = Comments{last: saved.Last}
	for _, c := range saved.Items {
		inDiff := c.InDiff == nil || *c.InDiff
		cs.items = append(cs.items, &Comment{
			ID: c.ID, Path: c.Path, Side: c.Side, Start: c.Start, End: c.End,
			Text: c.Text, Anchor: Anchor{InDiff: inDiff, Rev: c.Rev},
			code: c.Code, snippet: c.Snippet, noAnchor: c.InDiff == nil,
		})
	}
	return nil
}
