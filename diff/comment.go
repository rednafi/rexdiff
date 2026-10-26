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

// Comment is a review note on a range of lines on one side of a file. Use
// [NewComment] to make one. A comment keeps the text of its lines and its
// diff rows, for [Comments.Review]. As in herdr-reviewr, a comment does not
// move when its file changes. It stays on its line numbers.
type Comment struct {
	ID   int // [Comments.Add] sets it
	Path string
	Side Side
	// Start and End are the first and the last line of the range. They
	// start at 1.
	Start, End int
	Text       string
	// Anchor tells the view that the comment shows in.
	Anchor Anchor

	code    []string // the text of the lines Start to End
	snippet []string // the rows as unified diff lines, or empty to use code
	// noAnchor is true for a comment from a file with no anchors.
	noAnchor bool
}

// Anchor is the view that a comment is made in. A comment shows only in a
// view with the same anchor.
type Anchor struct {
	// InDiff is true for a comment on a diff, and false for a comment on
	// a file view.
	InDiff bool
	// Rev names the commits of the diff, or is "" for the working tree.
	Rev string
}

// NewComment returns a comment with no text on side s of the file at path.
// Rows are rows of the [Diff] of before and after. Start and End are the
// first and the last line of the rows on side s. They are 0 if no row has
// a line on s.
//
// In the diff rows of the comment, equal, deleted, and inserted lines start
// with " ", "-", and "+". The text comes from before and after, so tabs
// stay. In each block of changes, deleted lines come before inserted
// lines, also in the split layout. Continued rows add nothing.
func NewComment(path string, s Side, rows []Row, before, after string) Comment {
	c := Comment{Path: path, Side: s, snippet: snippet(rows, before, after)}
	for _, r := range rows {
		n := r.Line(s)
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
	lines := Lines(version(s, before, after))
	c.code = slices.Clone(lines[min(c.Start-1, len(lines)):min(c.End, len(lines))])
	return c
}

// Location returns the path and the lines of c, for example "a.go:3-4".
// A comment on the old side has " (removed)" at the end. [termtext.Quote]
// quotes the path.
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

// Comments is the set of comments of a review. The zero value is an empty
// set that is ready to use.
type Comments struct {
	last  int
	items []*Comment
}

// Len returns the number of comments.
func (cs *Comments) Len() int { return len(cs.items) }

// Add adds c with a new ID.
func (cs *Comments) Add(c Comment) {
	cs.last++
	c.ID = cs.last
	cs.items = append(cs.items, &c)
}

// Remove removes the comment with the given ID, if there is one.
func (cs *Comments) Remove(id int) {
	cs.items = slices.DeleteFunc(cs.items, func(c *Comment) bool { return c.ID == id })
}

// All returns the comments, sorted by path and then by start line.
func (cs *Comments) All() []*Comment {
	return slices.SortedStableFunc(slices.Values(cs.items), func(a, b *Comment) int {
		return cmp.Or(strings.Compare(a.Path, b.Path), cmp.Compare(a.Start, b.Start))
	})
}

// SetOldRev sets the Rev of each comment that [Comments.UnmarshalJSON]
// reads from the file of an older rexdiff. Such a file has no anchors, and
// its comments are on the diff of the scope that the file saves.
func (cs *Comments) SetOldRev(rev string) {
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

// At returns the first comment with anchor a whose range includes line, or
// nil.
func (cs *Comments) At(a Anchor, path string, s Side, line int) *Comment {
	for _, c := range cs.items {
		if c.Anchor == a && c.Path == path && c.Side == s &&
			line >= c.Start && line <= c.End {
			return c
		}
	}
	return nil
}

// EndingAt returns the comments with anchor a whose range ends on line.
func (cs *Comments) EndingAt(a Anchor, path string, s Side, line int) []*Comment {
	var ending []*Comment
	for _, c := range cs.items {
		if c.Anchor == a && c.Path == path && c.Side == s && c.End == line {
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

// version returns before for the [SideOld] side, and after for the
// [SideNew] side.
func version(s Side, before, after string) string {
	if s == SideOld {
		return before
	}
	return after
}

// savedComment is a [Comment] with its private fields, for MarshalJSON.
type savedComment struct {
	ID    int    `json:"id"`
	Path  string `json:"path"`
	Side  Side   `json:"side"`
	Start int    `json:"start"`
	End   int    `json:"end"`
	Text  string `json:"text"`
	// InDiff is nil in a file of an older rexdiff. Most comments of
	// such a file are on a diff.
	InDiff  *bool    `json:"in_diff"`
	Rev     string   `json:"rev"`
	Code    []string `json:"code"`
	Snippet []string `json:"snippet"`
}

// savedComments is a [Comments] with its private fields.
type savedComments struct {
	Last  int            `json:"last"`
	Items []savedComment `json:"items"`
}

// MarshalJSON encodes the comments with their code and their diff rows,
// so that UnmarshalJSON restores the same comments, for example after a
// restart. Bytes that are not UTF-8, for example in a Latin-1 file,
// become U+FFFD.
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

// UnmarshalJSON reads comments that MarshalJSON wrote.
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
