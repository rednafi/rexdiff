// Package tree builds the rows of the file list: a tree of directories
// that the user can expand and collapse.
package tree

import (
	"maps"
	"slices"
	"strings"
)

// Entry is a path in the file list.
type Entry struct {
	Path    string
	Changed bool // the file has changes
	Ignored bool // git ignores the path
	// Dir is true for an ignored directory that git lists as a single
	// path. Its entries are loaded when the user expands it.
	Dir bool
}

// Row is a directory or file shown in the file list.
type Row struct {
	Depth int // the 0-based depth of the row in the tree
	// Name is a directory or file name. A chain of directories that each
	// have only one child is shown in a single row, for example "a/b".
	Name string
	Dir  string // the path of a directory, or "" for a file
	File int    // the index of a file's entry, or -1 for a directory
	// Expanded is true for a directory whose children are shown.
	Expanded bool
	// Changed is true for a directory that contains a changed file.
	Changed bool
	Ignored bool // git ignores the path
}

// node is a directory in the tree.
type node struct {
	dirs    map[string]*node
	files   map[string]int // maps each file name to its entry index
	ignored bool
	changed bool
	// listed is true for a directory that comes from an Entry with Dir
	// set. Its row is always kept, so that the user can collapse it.
	listed bool
}

// newNode returns a directory with no entries.
func newNode() *node { return &node{dirs: map[string]*node{}, files: map[string]int{}} }

// Build returns the rows for es. Every directory is expanded if expandAll
// is true, and collapsed otherwise. toggled holds the paths of the
// directories that the user has switched to the opposite state.
// Directories come before files, and each group is sorted by name.
func Build(es []Entry, toggled map[string]bool, expandAll bool) []Row {
	root := newNode()
	for i, e := range es {
		insert(root, e, i)
	}
	b := builder{toggled: toggled, expandAll: expandAll, entries: es}
	b.flatten(root, "", 0)
	return b.rows
}

// builder collects the rows of a tree for [Build].
type builder struct {
	rows      []Row
	toggled   map[string]bool
	expandAll bool
	entries   []Entry
}

// insert adds entry e with index i to the tree under root.
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

// child returns the child directory of n called name, creating it if it
// does not exist.
func (n *node) child(name string) *node {
	c := n.dirs[name]
	if c == nil {
		c = newNode()
		n.dirs[name] = c
	}
	return c
}

// flatten appends the rows for the children of dir, whose path is prefix.
func (b *builder) flatten(dir *node, prefix string, depth int) {
	for _, name := range slices.Sorted(maps.Keys(dir.dirs)) {
		display, path, n := compress(name, join(prefix, name), dir.dirs[name])
		if fname, i, ok := loneFile(n); ok {
			// A chain that ends in a single file is shown as one file row.
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

// compress follows the chain of directories below n in which each has one
// child directory and no files, stopping at a listed directory. It returns
// the joined name, the path and the directory where it stopped.
func compress(name, path string, n *node) (string, string, *node) {
	for !n.listed && len(n.files) == 0 && len(n.dirs) == 1 {
		for c, sub := range n.dirs {
			name, path, n = name+"/"+c, path+"/"+c, sub
		}
	}
	return name, path, n
}

// loneFile returns the name and entry index of the only file in n. ok is
// true only if n is not listed and has exactly one file and no
// directories.
func loneFile(n *node) (name string, i int, ok bool) {
	if !n.listed && len(n.dirs) == 0 && len(n.files) == 1 {
		for name, i = range n.files {
			return name, i, true
		}
	}
	return "", 0, false
}

// fileRow returns the row for entry i at the given depth, with the given
// name.
func (b *builder) fileRow(depth int, name string, i int) Row {
	return Row{Depth: depth, Name: name, File: i, Ignored: b.entries[i].Ignored}
}

// join returns the path of name inside the directory prefix. An empty
// prefix means the root.
func join(prefix, name string) string {
	if prefix == "" {
		return name
	}
	return prefix + "/" + name
}
