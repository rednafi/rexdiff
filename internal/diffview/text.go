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

// tabSpaces is the text that replaces a tab.
var tabSpaces = strings.Repeat(" ", TabWidth)

// ExpandTabs replaces each tab in s with TabWidth spaces.
func ExpandTabs(s string) string { return strings.ReplaceAll(s, "\t", tabSpaces) }

// EachLine applies f to each line of s, split at the LFs, and joins the
// results with LFs.
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

// CRSymbol replaces a lone CR before highlighting, because chroma treats
// a CR as a line break. It is the picture that Escape shows for a CR.
const CRSymbol = "\u240d"

// The Unicode Control Pictures that Escape uses in place of control
// characters.
const (
	// The picture for U+0000. The pictures for U+0001 to U+001F follow it
	// in order.
	controlPictures = '\u2400'
	delPicture      = '\u2421'
)

// Escape replaces each control character with its Unicode Control Picture,
// so that the terminal displays it instead of acting on it. C1 control
// characters have no picture and become U+FFFD. Escape also shows each
// other invisible character, as [isHidden] reports, as its code point in
// angle brackets, for example <U+202E>. A bidirectional control would
// otherwise reorder the text that the terminal shows, so that code reads
// differently from how it runs, and a zero-width character would hide in
// plain sight. Bytes that are not valid UTF-8 become U+FFFD.
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

// needsEscape reports whether [Escape] replaces r.
func needsEscape(r rune) bool {
	if r < utf8.RuneSelf {
		return r <= 0x1f || r == ansi.DEL
	}
	return r <= 0x9f || isHidden(r)
}

// isHidden reports whether r is a character other than a control
// character that a terminal does not show as a mark of its own, or that
// changes the order of the text around it: a format character, such as
// U+202E (right-to-left override), U+00AD (soft hyphen) or a tag character
// (U+E0001 to U+E007F), a line or paragraph separator, or a filler, such as
// U+3164 (Hangul filler). It leaves out U+200C and U+200D (zero-width
// non-joiner and joiner), which some scripts and emoji sequences need. The
// variation selectors, which emoji need too, are not format characters.
func isHidden(r rune) bool {
	return r != '\u200c' && r != '\u200d' && !unicode.IsControl(r) && termtext.Invisible(r)
}

// CutLeft removes the first n cells of s, which can contain ANSI codes. If
// the cut falls inside a wide character, spaces replace the rest of that
// character, so the remaining text moves left by exactly n cells.
func CutLeft(s string, n int) string {
	if n <= 0 {
		return s // nothing to remove; a negative n would otherwise add spaces
	}
	want := max(ansi.StringWidth(s)-n, 0)
	cut := ansi.TruncateLeft(s, n, "")
	for ansi.StringWidth(cut) > want {
		n++
		cut = ansi.TruncateLeft(s, n, "")
	}
	return strings.Repeat(" ", want-ansi.StringWidth(cut)) + cut
}

// pad right-aligns s in a column of width cells.
func pad(s string, width int) string {
	w := ansi.StringWidth(s)
	if w >= width {
		return s
	}
	return strings.Repeat(" ", width-w) + s
}

// btoi returns 1 if b is true, and 0 otherwise.
func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}
