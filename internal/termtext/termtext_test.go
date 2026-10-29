package termtext_test

import (
	"testing"

	"github.com/rednafi/rexdiff/internal/termtext"
)

// TestQuote checks that Quote quotes only unsafe paths, and that it shows
// invisible characters as escapes: format characters, line and
// paragraph separators and fillers such as U+3164 (Hangul filler).
func TestQuote(t *testing.T) {
	t.Parallel()
	tests := []struct{ in, want string }{
		{"a.go", "a.go"},
		{"日本.go", "日本.go"},
		{"a\x1b]0;x\x07.go", `"a\x1b]0;x\a.go"`},
		{"a\n.go", `"a\n.go"`},
		{"a\x9b31m.go", `"a\x9b31m.go"`},
		{"a\u200b.go", `"a\u200b.go"`},
		{"a\u202eog.txt", `"a\u202eog.txt"`},
		{"a\u2066b\u2069.go", `"a\u2066b\u2069.go"`},
		{"\ufeffa.go", `"\ufeffa.go"`},
		{"a\u200d.go", `"a\u200d.go"`},
		{"a\u00adb.go", `"a\u00adb.go"`},
		{"a\U000e0041.go", `"a\U000e0041.go"`},
		{"a\u2028b\u2029.go", `"a\u2028b\u2029.go"`},
		{"a\u3164.go", `"a\u3164.go"`},
		{"\u115f\u1160\uffa0.go", `"\u115f\u1160\uffa0.go"`},
		{"a\x00\u3164\xff.go", `"a\x00\u3164\xff.go"`},
		{"braille\u2800.go", "braille\u2800.go"},
	}
	for _, tt := range tests {
		if got := termtext.Quote(tt.in); got != tt.want {
			t.Errorf("Quote(%q) = %s, want %s", tt.in, got, tt.want)
		}
	}
}

// TestPlain checks that Plain removes control characters and bidi
// controls, but keeps tabs, newlines and the joiners of emoji sequences.
func TestPlain(t *testing.T) {
	t.Parallel()
	tests := []struct{ in, want string }{
		{"a\tb\nc", "a\tb\nc"},
		{"a\x1b[31mb\r\n", "a[31mb\n"},
		{"x := \"\u202e // \u2066y\u2069\"", "x := \" // y\""},
		{"family 👨\u200d👩\u200d👧", "family 👨\u200d👩\u200d👧"},
		{"soft\u00adhyphen", "soft\u00adhyphen"},
	}
	for _, tt := range tests {
		if got := termtext.Plain(tt.in); got != tt.want {
			t.Errorf("Plain(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
