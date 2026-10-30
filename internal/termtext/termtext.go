// Package termtext makes untrusted text safe to display in a terminal.
package termtext

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Plain removes control characters except tabs and newlines. It also
// removes bidi controls, because they can reorder text. Other format
// characters stay, so a zero-width joiner still joins emoji.
func Plain(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' || !unicode.IsControl(r) && !unicode.Is(unicode.Bidi_Control, r) {
			return r
		}
		return -1
	}, s)
}

// Quote returns s as a quoted Go string if it has an [Invisible] character
// or is not valid UTF-8, as git does.
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

// Invisible reports whether a terminal shows r with no mark of its own.
func Invisible(r rune) bool {
	if r < utf8.RuneSelf {
		return r < ' ' || r == '\x7f'
	}
	return unicode.In(r, unicode.Cc, unicode.Cf, unicode.Zl, unicode.Zp,
		unicode.Other_Default_Ignorable_Code_Point)
}
