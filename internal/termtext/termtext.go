// Package termtext makes text from other people safe to show in a terminal.
package termtext

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Plain returns s without control characters. It keeps tabs and
// newlines. The printable rest of an escape sequence stays, as in
// herdr-reviewr, but it cannot change the terminal. Use Plain on text from
// other people before you show or send the text.
func Plain(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' || !unicode.IsControl(r) {
			return r
		}
		return -1
	}, s)
}

// Quote returns s in Go quotes if it has control characters or is not valid
// UTF-8. git shows such a path the same way. Then s stays on one line and
// cannot change the terminal.
func Quote(s string) string {
	if strings.ContainsFunc(s, unicode.IsControl) || !utf8.ValidString(s) {
		return strconv.Quote(s)
	}
	return s
}
