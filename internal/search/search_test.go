package search_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/rednafi/rexdiff/internal/search"
)

// TestFiles checks the order, limit and total count of the files that
// Files returns.
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
		{name: "empty query", paths: paths, limit: 50, want: paths, wantTotal: 4},
		{name: "recent first", paths: paths, recent: []string{"main.go", "b.go"}, limit: 50, want: []string{"main.go", "b.go", "a.go", "c/d.go"}, wantTotal: 4},
		{name: "a gone recent pick", paths: paths, recent: []string{"gone.go", "b.go"}, limit: 50, want: []string{"b.go", "a.go", "c/d.go", "main.go"}, wantTotal: 4},
		{name: "empty query limit", paths: paths, recent: []string{"main.go"}, limit: 2, want: []string{"main.go", "a.go"}, wantTotal: 4},
		{name: "fuzzy", q: "dgo", paths: paths, limit: 50, want: []string{"c/d.go"}, wantTotal: 1},
		{name: "recent does not rank a query", q: "go", paths: paths, recent: []string{"main.go"}, limit: 2, want: []string{"a.go", "b.go"}, wantTotal: 4},
		{name: "no match", q: "zzz", paths: paths, limit: 50, wantTotal: 0},
		// A query returns at most limit files, but counts every match.
		{name: "query limit", q: "f", paths: many, limit: search.MaxFiles, want: many[:search.MaxFiles], wantTotal: 60},
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

// TestMatches checks smart-case matching.
func TestMatches(t *testing.T) {
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
		{"ÉCOLE", "école", false}, // case is ignored only for ASCII letters
		{"école", "école", true},
		{"Éa", "Éa", true},
		{"\tx", "\tx", true},
		{"ab", "abc", false},
	}
	for _, tt := range tests {
		got := search.Matches(tt.text, tt.query)
		if got != tt.want {
			t.Errorf("Matches(%q, %q) = %v, want %v",
				tt.text, tt.query, got, tt.want)
		}
	}
}

// TestIndex checks the offset of the first smart-case match.
func TestIndex(t *testing.T) {
	t.Parallel()
	tests := []struct {
		text, query string
		want        int
	}{
		{"Foo bar foo", "foo", 0},
		{"xx FOO", "foo", 3},
		{"foo Foo", "Foo", 4},
		{"éA", "a", 2}, // offsets count bytes
		{"abc", "", -1},
		{"abc", "d", -1},
	}
	for _, tt := range tests {
		if got := search.Index(tt.text, tt.query); got != tt.want {
			t.Errorf("Index(%q, %q) = %d, want %d", tt.text, tt.query, got, tt.want)
		}
	}
}

// longFind returns a line of about 2 MB and a 1001-byte query that
// almost matches it everywhere, the slowest input for a naive search.
func longFind() (text, query string) {
	return strings.Repeat("a", 2_000_000), strings.Repeat("a", 1000) + "b"
}

// TestIndexLong checks a query that almost matches a long line at every
// offset.
func TestIndexLong(t *testing.T) {
	t.Parallel()
	text, query := longFind()
	if got := search.Index(text, query); got != -1 {
		t.Errorf("Index = %d, want -1", got)
	}
	if got := search.Index(text+"A"+query, query); got != len(text)+1 {
		t.Errorf("Index = %d, want %d", got, len(text)+1)
	}
}

// BenchmarkMatchesLong measures a search in a long line, as the find bar
// does in a minified file.
func BenchmarkMatchesLong(b *testing.B) {
	text, query := longFind()
	for _, q := range []string{query, strings.ToUpper(query)} {
		b.Run(fmt.Sprintf("upper=%v", q != query), func(b *testing.B) {
			for b.Loop() {
				search.Matches(text, q)
			}
		})
	}
}

// TestIndices checks that Indices returns the indices of the matching
// labels, best match first.
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
