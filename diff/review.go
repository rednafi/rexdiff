package diff

import (
	"strings"

	"github.com/rednafi/rexdiff/internal/termtext"
)

// Review returns the comments as text for a coding agent. The text has no
// control characters.
//
// A comment looks like this:
//
//	a.go:3-4
//	 x := 1
//	+y := 2
//	Merge these lines.
//
// If stale reports true for a comment, " (stale)" follows its location.
func (cs *Comments) Review(prefix string, stale func(*Comment) bool) string {
	var parts []string
	if p := strings.TrimRight(termtext.Plain(prefix), " \n\t"); p != "" {
		parts = append(parts, p)
	}
	for _, c := range cs.All() {
		parts = append(parts, formatComment(c, stale != nil && stale(c)))
	}
	return strings.Join(parts, "\n\n")
}

func formatComment(c *Comment, stale bool) string {
	var b strings.Builder
	b.WriteString(termtext.Plain(c.Location()))
	if stale {
		b.WriteString(" (stale)")
	}
	if len(c.snippet) > 0 {
		for _, l := range c.snippet {
			b.WriteByte('\n')
			b.WriteString(termtext.Plain(l))
		}
	} else {
		mark := "+"
		if c.Side == SideOld {
			mark = "-"
		}
		for _, l := range c.code {
			b.WriteByte('\n')
			b.WriteString(mark)
			b.WriteString(termtext.Plain(l))
		}
	}
	var text []string
	for l := range strings.SplitSeq(termtext.Plain(c.Text), "\n") {
		if strings.TrimSpace(l) != "" {
			text = append(text, strings.TrimRight(l, " \t"))
		}
	}
	b.WriteByte('\n')
	b.WriteString(strings.Join(text, "\n"))
	return b.String()
}

func snippet(rows []Row, oldText, newText string) []string {
	oldLines, newLines := SplitLines(oldText), SplitLines(newText)
	line := func(ls []string, n int) string {
		if n >= 1 && n-1 < len(ls) {
			return ls[n-1]
		}
		return ""
	}
	var out, minus, plus []string
	flush := func() {
		out = append(append(out, minus...), plus...)
		minus, plus = nil, nil
	}
	for _, r := range rows {
		if r.Continued {
			continue
		}
		switch r.Kind {
		case KindEqual:
			flush()
			out = append(out, " "+line(newLines, r.New))
		case KindAdded:
			plus = append(plus, "+"+line(newLines, r.New))
		case KindDeleted:
			minus = append(minus, "-"+line(oldLines, r.Old))
		case KindChanged:
			minus = append(minus, "-"+line(oldLines, r.Old))
			plus = append(plus, "+"+line(newLines, r.New))
		}
	}
	flush()
	return out
}
