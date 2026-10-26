// Package tree makes the rows of the file list. The rows show the files
// in a tree of directories that the user can expand and collapse. The
// rules come from herdr-reviewr.
package tree

import (
	"maps"
	"slices"
	"strings"
)

// Entry is a path in the file list.
type Entry struct {
	Path    string
	Changed bool // the file has a change
	Ignored bool // git ignores the path
	// Dir is true for an ignored directory that git lists as one path.
	// Its entries load when the user expands it.
	Dir bool
}

// Row is a directory or a file that the file list shows.
type Row struct {
	Depth int // the level of the row in the tree, from 0
	// Name is a directory name or a file name. A chain of directories that
	// each have one child shows in one row, for example "a/b".
	Name string
	Dir  string // the path of a directory, or "" for a file
	File int    // the index of the entry of a file, or -1 for a directory
	// Expanded is true for a directory that shows its children.
	Expanded bool
	// Changed is true for a directory that holds a changed file.
	Changed bool
	Ignored bool // git ignores the path
}

// node is a directory of the tree.
type node struct {
	dirs    map[string]*node
	files   map[string]int // the entry index of each file, by name
	ignored bool
	changed bool
	// listed is true for a directory from an Entry with Dir. Its row
	// stays, so that the user can collapse it.
	listed bool
}

// newNode returns a directory with no entries.
func newNode() *node { return &node{dirs: map[string]*node{}, files: map[string]int{}} }

// Build returns the rows of entries. Each directory starts expanded if
// expanded is true, else collapsed. toggled has the paths of the
// directories that the user changes from that start. A directory shows
// before the files, and each group is in alphabetical order.
func Build(entries []Entry, toggled map[string]bool, expanded bool) []Row {
	root := newNode()
	for i, e := range entries {
		insert(root, e, i)
	}
	b := builder{toggled: toggled, expanded: expanded, entries: entries}
	b.flatten(root, "", 0)
	return b.rows
}

// builder makes the rows of a tree. The fields are the arguments of
// [Build].
type builder struct {
	rows     []Row
	toggled  map[string]bool
	expanded bool
	entries  []Entry
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

// child returns the directory name of n. It adds the directory if n does
// not have it.
func (n *node) child(name string) *node {
	c := n.dirs[name]
	if c == nil {
		c = newNode()
		n.dirs[name] = c
	}
	return c
}

// flatten adds the rows of the children of dir at path prefix.
func (b *builder) flatten(dir *node, prefix string, depth int) {
	for _, name := range slices.Sorted(maps.Keys(dir.dirs)) {
		display, path, n := compress(name, join(prefix, name), dir.dirs[name])
		if fname, i, ok := loneFile(n); ok {
			// A chain that ends in one file shows as one file row.
			b.rows = append(b.rows, b.fileRow(depth, display+"/"+fname, i))
			continue
		}
		open := b.expanded != b.toggled[path]
		b.rows = append(b.rows, Row{
			Depth:    depth,
			Name:     display,
			Dir:      path,
			File:     -1,
			Expanded: open,
			Changed:  n.changed,
			Ignored:  n.ignored,
		})
		if open {
			b.flatten(n, path, depth+1)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(dir.files)) {
		b.rows = append(b.rows, b.fileRow(depth, name, dir.files[name]))
	}
}

// compress follows the directories with one child and no files from n. It
// stops at a listed directory. It returns the joined name, the path, and
// the directory where it stops.
func compress(name, path string, n *node) (string, string, *node) {
	for !n.listed && len(n.files) == 0 && len(n.dirs) == 1 {
		for c, sub := range n.dirs {
			name, path, n = name+"/"+c, path+"/"+c, sub
		}
	}
	return name, path, n
}

// loneFile returns the name and the index of the file of n, if n is not
// listed and has one file and no directories.
func loneFile(n *node) (name string, i int, ok bool) {
	if !n.listed && len(n.dirs) == 0 && len(n.files) == 1 {
		for name, i := range n.files {
			return name, i, true
		}
	}
	return "", 0, false
}

// fileRow returns the row of entry i at depth, with the name name.
func (b *builder) fileRow(depth int, name string, i int) Row {
	return Row{Depth: depth, Name: name, File: i, Ignored: b.entries[i].Ignored}
}

// join returns the path of name in the directory prefix. An empty prefix
// is the root.
func join(prefix, name string) string {
	if prefix == "" {
		return name
	}
	return prefix + "/" + name
}
