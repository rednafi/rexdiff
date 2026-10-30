// Package tree builds the rows of the file list.
package tree

import (
	"maps"
	"slices"
	"strings"
)

// Entry is a path in the file list.
type Entry struct {
	Path    string
	Changed bool
	Ignored bool
	// Dir is true for an ignored directory that git lists as one path.
	Dir bool
}

// Row is a directory or file shown in the file list.
type Row struct {
	Depth int
	// Name can join a chain of single-child directories, for example "a/b".
	Name     string
	Dir      string // "" for a file
	File     int    // entry index, or -1 for a directory
	Expanded bool
	Changed  bool
	Ignored  bool
}

type node struct {
	dirs    map[string]*node
	files   map[string]int
	ignored bool
	changed bool
	// listed marks an Entry with Dir set. Its row always stays, so that the
	// user can collapse it.
	listed bool
}

func newNode() *node { return &node{dirs: map[string]*node{}, files: map[string]int{}} }

// Build returns the rows for es. toggled holds the directories whose state
// is the opposite of expandAll.
func Build(es []Entry, toggled map[string]bool, expandAll bool) []Row {
	root := newNode()
	for i, e := range es {
		insert(root, e, i)
	}
	b := builder{toggled: toggled, expandAll: expandAll, entries: es}
	b.flatten(root, "", 0)
	return b.rows
}

type builder struct {
	rows      []Row
	toggled   map[string]bool
	expandAll bool
	entries   []Entry
}

func insert(root *node, e Entry, i int) {
	parts := strings.FieldsFunc(e.Path, func(r rune) bool { return r == '/' })
	if e.Dir {
		cur := root
		for _, p := range parts {
			cur = cur.child(p)
		}
		cur.ignored, cur.listed = e.Ignored, true
		return
	}
	if len(parts) == 0 {
		return
	}
	cur := root
	for _, p := range parts[:len(parts)-1] {
		cur = cur.child(p)
		cur.changed = cur.changed || e.Changed
	}
	cur.files[parts[len(parts)-1]] = i
}

func (n *node) child(name string) *node {
	c := n.dirs[name]
	if c == nil {
		c = newNode()
		n.dirs[name] = c
	}
	return c
}

func (b *builder) flatten(dir *node, prefix string, depth int) {
	for _, name := range slices.Sorted(maps.Keys(dir.dirs)) {
		display, path, n := compress(name, join(prefix, name), dir.dirs[name])
		if fname, i, ok := loneFile(n); ok {
			b.rows = append(b.rows, b.fileRow(depth, display+"/"+fname, i))
			continue
		}
		expanded := b.expandAll != b.toggled[path]
		b.rows = append(b.rows, Row{
			Depth:    depth,
			Name:     display,
			Dir:      path,
			File:     -1,
			Expanded: expanded,
			Changed:  n.changed,
			Ignored:  n.ignored,
		})
		if expanded {
			b.flatten(n, path, depth+1)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(dir.files)) {
		b.rows = append(b.rows, b.fileRow(depth, name, dir.files[name]))
	}
}

func compress(name, path string, n *node) (string, string, *node) {
	for !n.listed && len(n.files) == 0 && len(n.dirs) == 1 {
		for c, sub := range n.dirs {
			name, path, n = name+"/"+c, path+"/"+c, sub
		}
	}
	return name, path, n
}

func loneFile(n *node) (name string, i int, ok bool) {
	if !n.listed && len(n.dirs) == 0 && len(n.files) == 1 {
		for name, i = range n.files {
			return name, i, true
		}
	}
	return "", 0, false
}

func (b *builder) fileRow(depth int, name string, i int) Row {
	return Row{Depth: depth, Name: name, File: i, Ignored: b.entries[i].Ignored}
}

func join(prefix, name string) string {
	if prefix == "" {
		return name
	}
	return prefix + "/" + name
}
