package termtext_test

import (
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/rednafi/rexdiff/internal/termtext"
)

// FuzzPlain checks that Plain and Quote remove all unsafe control characters.
func FuzzPlain(f *testing.F) {
	for _, s := range []string{
		"a\x00b\n",
		"\tx\r\n",
		"\x1b[31mred\x1b[0m\n",
		"\x1b]52;c;aGVsbG8=\x07\n",
		"\u009b31m\u009d0;title\u009c\n",
		"\x9b31m\xff\xfe\n",
		"日本語😀👨‍👩‍👧\u202e\n",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		p := termtext.Plain(s)
		unsafe := func(r rune) bool { return r != '\t' && r != '\n' && unicode.IsControl(r) }
		if strings.ContainsFunc(p, unsafe) {
			t.Errorf("Plain(%q) = %q, want no control characters", s, p)
		}
		if pp := termtext.Plain(p); pp != p {
			t.Errorf("Plain(Plain(%q)) = %q, want %q", s, pp, p)
		}
		q := termtext.Quote(s)
		if strings.ContainsFunc(q, unicode.IsControl) || !utf8.ValidString(q) {
			t.Errorf("Quote(%q) = %q, want valid UTF-8 with no control characters",
				s, q)
		}
	})
}
