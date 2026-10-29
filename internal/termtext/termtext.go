// Package termtext makes untrusted text safe to display in a terminal.
package termtext

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Plain returns s with all control characters removed except tabs and
// newlines, and with the bidirectional controls removed, such as U+202E
// (right-to-left override), because they can reorder the text around
// them. The printable characters of an escape sequence remain, but they
// can no longer control the terminal. Other format characters remain,
// so that a zero-width joiner still joins an emoji sequence.
func Plain(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' || !unicode.IsControl(r) && !unicode.Is(unicode.Bidi_Control, r) {
			return r
		}
		return -1
	}, s)
}

// Quote returns s as a quoted Go string if it contains an invisible
// character, as [Invisible] reports, or is not valid UTF-8, as git does
// for such paths. Otherwise it returns s unchanged. The quoted string
// shows each invisible character as an escape, so that a name with one,
// such as U+200B (zero-width space), does not look like another name.
func Quote(s string) string {
	if !strings.ContainsFunc(s, Invisible) && utf8.ValidString(s) {
		return s
	}
	q := strconv.Quote(s)
	if !strings.ContainsFunc(q, Invisible) {
		return q
	}
	// strconv.Quote keeps the fillers, such as U+3164, because Unicode
	// counts them as letters. Escape them in the same way.
	var b strings.Builder
	for _, r := range q {
		if Invisible(r) {
			fmt.Fprintf(&b, `\u%04x`, r)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Invisible reports whether r is a character that a terminal does not
// show as a mark of its own: a control character, a format character, a
// line or paragraph separator, or a character that Unicode says to ignore
// when displaying text, such as U+3164 (Hangul filler). A terminal can show
// such a character as nothing or as blank space, or drop it.
func Invisible(r rune) bool {
	if r < utf8.RuneSelf {
		return r < ' ' || r == '\x7f'
	}
	return unicode.In(r, unicode.Cc, unicode.Cf, unicode.Zl, unicode.Zp,
		unicode.Other_Default_Ignorable_Code_Point)
}
