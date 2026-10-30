package tree_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/rednafi/rexdiff/internal/tree"
)

func changed(paths ...string) []tree.Entry {
	var es []tree.Entry
	for _, p := range paths {
		es = append(es, tree.Entry{Path: p, Changed: true})
	}
	return es
}

func plain(paths ...string) []tree.Entry {
	var es []tree.Entry
	for _, p := range paths {
		es = append(es, tree.Entry{Path: p})
	}
	return es
}

func ignoredDir(path string) tree.Entry {
	return tree.Entry{Path: path, Ignored: true, Dir: true}
}

func shape(rows []tree.Row) []string {
	var out []string
	for _, r := range rows {
		kind := "file"
		if r.File < 0 {
			kind = "dir"
		}
		var b strings.Builder
		fmt.Fprintf(&b, "%d:%s:%s", r.Depth, kind, r.Name)
		for _, m := range []struct {
			on   bool
			mark string
		}{{r.Changed, "+"}, {r.Ignored, "!"}, {r.Expanded, "v"}} {
			if m.on {
				b.WriteString(" ")
				b.WriteString(m.mark)
			}
		}
		out = append(out, b.String())
	}
	return out
}

func set(paths ...string) map[string]bool {
	m := map[string]bool{}
	for _, p := range paths {
		m[p] = true
	}
	return m
}

func TestBuild(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		entries  []tree.Entry
		toggled  map[string]bool
		expanded bool
		want     []string
	}{
		{
			name:     "directories before files",
			entries:  changed("src/app.rs", "src/ui.rs", "Cargo.toml"),
			expanded: true,
			want: []string{
				"0:dir:src + v", "1:file:app.rs", "1:file:ui.rs",
				"0:file:Cargo.toml",
			},
		},
		{
			name:     "a chain of one child ends in a file row",
			entries:  changed("docs/plans/2026/plan.md"),
			expanded: true,
			want:     []string{"0:file:docs/plans/2026/plan.md"},
		},
		{
			name:     "a chain stops at a branch",
			entries:  changed("a/b/c/one.rs", "a/b/c/two.rs"),
			expanded: true,
			want:     []string{"0:dir:a/b/c + v", "1:file:one.rs", "1:file:two.rs"},
		},
		{
			name:     "a collapsed directory hides its children",
			entries:  changed("src/app.rs", "src/ui.rs"),
			toggled:  set("src"),
			expanded: true,
			want:     []string{"0:dir:src +"},
		},
		{
			name:    "the Files tab starts collapsed",
			entries: changed("src/app.rs", "src/ui.rs"),
			want:    []string{"0:dir:src +"},
		},
		{
			name:    "a toggle expands in the Files tab",
			entries: changed("src/app.rs", "src/ui.rs"),
			toggled: set("src"),
			want:    []string{"0:dir:src + v", "1:file:app.rs", "1:file:ui.rs"},
		},
		{
			name:    "only a directory with a change has the mark",
			entries: append(changed("src/ui.rs"), plain("src/app.rs", "docs/a.md", "docs/b.md")...),
			want:    []string{"0:dir:docs", "0:dir:src +"},
		},
		{
			name:    "the mark stays when the directory expands",
			entries: append(changed("src/ui.rs"), plain("src/app.rs", "docs/a.md", "docs/b.md")...),
			toggled: set("src"),
			want: []string{
				"0:dir:docs", "0:dir:src + v", "1:file:app.rs",
				"1:file:ui.rs",
			},
		},
		{
			name:    "a deep change marks each directory above it",
			entries: append(changed("a/b/deep.rs"), plain("a/top.rs", "a/b/other.rs")...),
			toggled: set("a"),
			want:    []string{"0:dir:a + v", "1:dir:b +", "1:file:top.rs"},
		},
		{
			name:    "an ignored directory starts collapsed",
			entries: []tree.Entry{ignoredDir("target")},
			want:    []string{"0:dir:target !"},
		},
		{
			name: "the entries of an expanded ignored directory show under it",
			entries: []tree.Entry{
				ignoredDir("target"),
				{Path: "target/a.o", Ignored: true},
				{Path: "target/b.o", Ignored: true},
			},
			toggled: set("target"),
			want:    []string{"0:dir:target ! v", "1:file:a.o !", "1:file:b.o !"},
		},
		{
			name: "an ignored directory with one child keeps its row",
			entries: []tree.Entry{
				ignoredDir("a"),
				ignoredDir("a/b"),
				{Path: "a/b/c.o", Ignored: true},
			},
			toggled: set("a", "a/b"),
			want:    []string{"0:dir:a ! v", "1:dir:b ! v", "2:file:c.o !"},
		},
		{
			name:    "an ignored file",
			entries: []tree.Entry{{Path: "build.log", Ignored: true}},
			want:    []string{"0:file:build.log !"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := shape(tree.Build(tt.entries, tt.toggled, tt.expanded))
			if !slices.Equal(got, tt.want) {
				t.Errorf("Build = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBuildIndex(t *testing.T) {
	t.Parallel()
	es := append(changed("z.rs", "a/b/deep.rs"), plain("a/top.rs", "a/b/other.rs")...)
	rows := tree.Build(es, nil, true)
	type place struct {
		dir  string
		file int
	}
	var got []place
	for _, r := range rows {
		got = append(got, place{r.Dir, r.File})
	}
	// Directories come first and names are sorted, so z.rs is last.
	want := []place{{"a", -1}, {"a/b", -1}, {"", 1}, {"", 3}, {"", 2}, {"", 0}}
	if !slices.Equal(got, want) {
		t.Errorf("Build places = %v, want %v (rows %q)", got, want, shape(rows))
	}
}
