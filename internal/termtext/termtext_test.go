package termtext_test

import (
	"testing"

	"github.com/rednafi/rexdiff/internal/termtext"
)

// TestQuote checks that Quote quotes only the paths that are not safe.
func TestQuote(t *testing.T) {
	t.Parallel()
	tests := []struct{ in, want string }{
		{"a.go", "a.go"},
		{"日本.go", "日本.go"},
		{"a\x1b]0;x\x07.go", `"a\x1b]0;x\a.go"`},
		{"a\n.go", `"a\n.go"`},
		{"a\x9b31m.go", `"a\x9b31m.go"`},
	}
	for _, tt := range tests {
		if got := termtext.Quote(tt.in); got != tt.want {
			t.Errorf("Quote(%q) = %s, want %s", tt.in, got, tt.want)
		}
	}
}
