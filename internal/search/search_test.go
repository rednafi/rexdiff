package search_test

import (
	"fmt"
	"slices"
	"testing"

	"github.com/rednafi/rexdiff/internal/search"
)

// TestFiles checks the order and the limit of the files.
func TestFiles(t *testing.T) {
	t.Parallel()
	paths := []string{"a.go", "b.go", "c/d.go", "main.go"}
	var many []string
	for i := range 60 {
		many = append(many, fmt.Sprintf("f%02d.txt", i))
	}
	tests := []struct {
		name, q   string
		paths     []string
		recent    []string
		limit     int
		want      []string
		wantTotal int
	}{
		{"empty query", "", paths, nil, 50, paths, 4},
		{"recent first", "", paths, []string{"main.go", "b.go"}, 50, []string{"main.go", "b.go", "a.go", "c/d.go"}, 4},
		{"a gone recent pick", "", paths, []string{"gone.go", "b.go"}, 50, []string{"b.go", "a.go", "c/d.go", "main.go"}, 4},
		{"empty query limit", "", paths, []string{"main.go"}, 2, []string{"main.go", "a.go"}, 4},
		{"fuzzy", "dgo", paths, nil, 50, []string{"c/d.go"}, 1},
		{"recent does not rank a query", "go", paths, []string{"main.go"}, 2, []string{"a.go", "b.go"}, 4},
		{"no match", "zzz", paths, nil, 50, nil, 0},
		// A query shows at most the limit, and counts all the matches.
		{"query limit", "f", many, nil, search.MaxFiles, many[:search.MaxFiles], 60},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, total := search.Files(tt.paths, tt.recent, tt.q, tt.limit)
			if !slices.Equal(got, tt.want) || total != tt.wantTotal {
				t.Errorf("Files(%q, %q) = %q, %d, want %q, %d",
					tt.q, tt.recent, got, total, tt.want, tt.wantTotal)
			}
		})
	}
}

// TestMatch checks the smart case of herdr-reviewr.
func TestMatch(t *testing.T) {
	t.Parallel()
	tests := []struct {
		text, query string
		want        bool
	}{
		{"Foo bar", "foo", true},
		{"foo bar", "Foo", false},
		{"Foo bar", "Foo", true},
		{"FOO", "foo", true},
		{"x := 1", "", false},
		{"", "a", false},
		{"ÉCOLE", "école", false}, // only ASCII ignores case
		{"école", "école", true},
		{"Éa", "Éa", true},
		{"\tx", "\tx", true},
		{"ab", "abc", false},
	}
	for _, tt := range tests {
		got := search.Match(tt.text, tt.query)
		if got != tt.want {
			t.Errorf("Match(%q, %q) = %v, want %v",
				tt.text, tt.query, got, tt.want)
		}
	}
}

// TestIndices checks that Indices returns the indices of the labels that
// match, the best match first.
func TestIndices(t *testing.T) {
	t.Parallel()
	labels := []string{"main feat", "dev", "feature x"}
	if got, want := search.Indices("feat", labels), []int{2, 0}; !slices.Equal(got, want) {
		t.Errorf("Indices(%q, %q) = %v, want %v", "feat", labels, got, want)
	}
	if got := search.Indices("zzz", labels); len(got) != 0 {
		t.Errorf("Indices(%q, %q) = %v, want none", "zzz", labels, got)
	}
}
