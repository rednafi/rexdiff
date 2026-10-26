package diff

import (
	"strings"

	"github.com/rednafi/rexdiff/internal/termtext"
)

// Review returns the comments as herdr-reviewr text for a coding agent. It
// writes the prefix, if not empty, then the comments in [Comments.All] order.
// Blank lines separate them. The text has no control characters.
//
// This is an example of a comment:
//
//	a.go:3-4
//	 x := 1
//	+y := 2
//	Merge these lines.
func (cs *Comments) Review(prefix string) string {
	var parts []string
	if p := strings.TrimRight(termtext.Plain(prefix), " \n\t"); p != "" {
		parts = append(parts, p)
	}
	for _, c := range cs.All() {
		parts = append(parts, format(c))
	}
	return strings.Join(parts, "\n\n")
}

// format returns c as text for [Comments.Review].
func format(c *Comment) string {
	var b strings.Builder
	b.WriteString(termtext.Plain(c.Location()))
	// Write the rows as the diff shows them, so that the agent reads a real
	// diff.
	if len(c.snippet) > 0 {
		for _, l := range c.snippet {
			b.WriteString("\n" + termtext.Plain(l))
		}
	} else {
		mark := "+"
		if c.Side == SideOld {
			mark = "-"
		}
		for _, l := range c.code {
			b.WriteString("\n" + mark + termtext.Plain(l))
		}
	}
	var text []string
	for l := range strings.SplitSeq(termtext.Plain(c.Text), "\n") {
		if strings.TrimSpace(l) != "" {
			text = append(text, strings.TrimRight(l, " \t"))
		}
	}
	b.WriteString("\n" + strings.Join(text, "\n"))
	return b.String()
}

// snippet returns rows as unified diff lines. See [NewComment].
func snippet(rows []Row, before, after string) []string {
	oldLines, newLines := Lines(before), Lines(after)
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
		case KindInsert:
			plus = append(plus, "+"+line(newLines, r.New))
		case KindDelete:
			minus = append(minus, "-"+line(oldLines, r.Old))
		case KindChange:
			minus = append(minus, "-"+line(oldLines, r.Old))
			plus = append(plus, "+"+line(newLines, r.New))
		}
	}
	flush()
	return out
}
