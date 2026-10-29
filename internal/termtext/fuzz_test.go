package termtext_test

import (
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/rednafi/rexdiff/internal/termtext"
)

// FuzzPlain checks that Plain removes every control character except tabs
// and newlines and every bidi control, that Plain is idempotent, and that
// Quote returns valid UTF-8 with no invisible characters: no control or
// format characters, line or paragraph separators or fillers.
func FuzzPlain(f *testing.F) {
	for _, s := range []string{
		"a\x00b\n",
		"\tx\r\n",
		"\x1b[31mred\x1b[0m\n",
		"\x1b]52;c;aGVsbG8=\x07\n",
		"\u009b31m\u009d0;title\u009c\n",
		"\x9b31m\xff\xfe\n",
		"日本語😀👨‍👩‍👧\u202e\n",
		"a\u3164b\u2028c\u00ad\U000e0041\n",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		p := termtext.Plain(s)
		isUnsafe := func(r rune) bool {
			return r != '\t' && r != '\n' && unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r)
		}
		if strings.ContainsFunc(p, isUnsafe) {
			t.Errorf("Plain(%q) = %q, want no control characters", s, p)
		}
		if pp := termtext.Plain(p); pp != p {
			t.Errorf("Plain(Plain(%q)) = %q, want %q", s, pp, p)
		}
		q := termtext.Quote(s)
		isHidden := func(r rune) bool {
			return unicode.In(r, unicode.Cc, unicode.Cf, unicode.Zl, unicode.Zp,
				unicode.Other_Default_Ignorable_Code_Point)
		}
		if strings.ContainsFunc(q, isHidden) || !utf8.ValidString(q) {
			t.Errorf("Quote(%q) = %q, want valid UTF-8 with no invisible characters",
				s, q)
		}
	})
}
