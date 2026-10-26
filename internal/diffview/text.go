// Derived from charmbracelet/crush (FSL-1.1-MIT). See ../DERIVED.md.

package diffview

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

// TabWidth is the number of spaces that replace a tab.
const TabWidth = 4

// The Unicode Control Pictures that Escape shows in place of a control
// character.
const (
	// The picture of U+0000. The pictures of U+0001 to U+001F follow it.
	controlPictures = '\u2400'
	delPicture      = '\u2421'
)

// Escape replaces each control character with its Unicode Control Picture,
// so the terminal shows it and does not run it. A C1 control becomes
// U+FFFD, because it has no picture.
func Escape(content string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r <= 0x1f: // C0 controls
			return controlPictures + r
		case r == ansi.DEL:
			return delPicture
		case r >= 0x80 && r <= 0x9f: // C1 controls
			return utf8.RuneError
		}
		return r
	}, content)
}

// CutLeft removes the first n cells of s, which can have ANSI codes. If
// the cut is inside a wide character, a space takes the place of the rest
// of that character. Thus the text after the cut moves by n cells.
func CutLeft(s string, n int) string {
	want := max(ansi.StringWidth(s)-n, 0)
	cut := ansi.TruncateLeft(s, n, "")
	for ansi.StringWidth(cut) > want {
		n++
		cut = ansi.TruncateLeft(s, n, "")
	}
	return strings.Repeat(" ", want-ansi.StringWidth(cut)) + cut
}

// pad right-aligns v in a column of width cells.
func pad(v any, width int) string {
	s := fmt.Sprint(v)
	w := ansi.StringWidth(s)
	if w >= width {
		return s
	}
	return strings.Repeat(" ", width-w) + s
}

// btoi returns 1 if b is true, else 0.
func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}
