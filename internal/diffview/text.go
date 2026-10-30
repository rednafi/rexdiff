// Derived from charmbracelet/crush (FSL-1.1-MIT). See ../DERIVED.md.

package diffview

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"

	"github.com/rednafi/rexdiff/internal/termtext"
)

// TabWidth is the number of spaces that replace a tab.
const TabWidth = 4

var tabSpaces = strings.Repeat(" ", TabWidth)

// ExpandTabs replaces each tab in s with TabWidth spaces.
func ExpandTabs(s string) string { return strings.ReplaceAll(s, "\t", tabSpaces) }

// EachLine applies f to each LF-separated line of s.
func EachLine(s string, f func(string) string) string {
	if !strings.Contains(s, "\n") {
		return f(s)
	}
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = f(l)
	}
	return strings.Join(lines, "\n")
}

// CRSymbol replaces a CR before highlighting, because chroma treats a CR
// as a line break.
const CRSymbol = "\u240d"

const (
	// U+0001 to U+001F follow in order.
	controlPictures = '\u2400'
	delPicture      = '\u2421'
)

// Escape makes control and invisible characters visible. This stops
// terminal escapes, bidi reordering and hidden text.
func Escape(content string) string {
	if utf8.ValidString(content) && !strings.ContainsFunc(content, needsEscape) {
		return content
	}
	var b strings.Builder
	b.Grow(len(content))
	for _, r := range content {
		switch {
		case r <= 0x1f: // C0 controls
			b.WriteRune(controlPictures + r)
		case r == ansi.DEL:
			b.WriteRune(delPicture)
		case r >= 0x80 && r <= 0x9f: // C1 controls
			b.WriteRune(utf8.RuneError)
		case isHidden(r):
			fmt.Fprintf(&b, "<U+%04X>", r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func needsEscape(r rune) bool {
	if r < utf8.RuneSelf {
		return r <= 0x1f || r == ansi.DEL
	}
	return r <= 0x9f || isHidden(r)
}

// isHidden leaves out U+200C and U+200D, because some scripts and emoji
// need them.
func isHidden(r rune) bool {
	return r != '\u200c' && r != '\u200d' && !unicode.IsControl(r) && termtext.Invisible(r)
}

// CutLeft removes the first n cells of s. A cut wide character becomes
// spaces, so the text moves left by exactly n.
func CutLeft(s string, n int) string {
	if n <= 0 {
		return s // a negative n would add spaces
	}
	want := max(ansi.StringWidth(s)-n, 0)
	cut := ansi.TruncateLeft(s, n, "")
	for ansi.StringWidth(cut) > want {
		n++
		cut = ansi.TruncateLeft(s, n, "")
	}
	return strings.Repeat(" ", want-ansi.StringWidth(cut)) + cut
}

func pad(s string, width int) string {
	w := ansi.StringWidth(s)
	if w >= width {
		return s
	}
	return strings.Repeat(" ", width-w) + s
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}
