package diffview_test

import (
	"testing"

	"github.com/rednafi/rexdiff/internal/diffview"
)

// Escape must keep the joiners and variation selectors that emoji need.
// It must also keep visible blanks such as U+2800 (Braille pattern blank).
func TestEscapeInvisible(t *testing.T) {
	t.Parallel()
	tests := []struct{ in, want string }{
		{"a\u3164b", "a<U+3164>b"},
		{"\u115f\u1160\uffa0", "<U+115F><U+1160><U+FFA0>"},
		{"a\u2028b\u2029c", "a<U+2028>b<U+2029>c"},
		{"soft\u00adhyphen", "soft<U+00AD>hyphen"},
		{"tag\U000e0041\U000e007f", "tag<U+E0041><U+E007F>"},
		{"a\u180eb", "a<U+180E>b"},
		{"👨\u200d👩\u200d👧 ❤\ufe0f", "👨\u200d👩\u200d👧 ❤\ufe0f"},
		{"a\u200cb", "a\u200cb"},
		{"braille\u2800", "braille\u2800"},
		{"a\x00b\x7f\u0085", "a\u2400b\u2421\ufffd"},
	}
	for _, tt := range tests {
		if got := diffview.Escape(tt.in); got != tt.want {
			t.Errorf("Escape(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
