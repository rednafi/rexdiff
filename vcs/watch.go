package vcs

import (
	"context"
	"encoding/binary"
	"hash"
	"hash/fnv"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
)

// pauseFactor sets the pause after a check of a [Watcher]. The next check
// waits this many times as long as the last check took. Thus the checks
// use less than 1/21 of a core. herdr-reviewr uses the same factor.
const pauseFactor = 20

// Watcher reports whether the working tree or the refs can have changed. It
// compares the file status of the files that git lists, of their
// directories, and of the git files. While nothing changes, it runs no
// git process. After a change, it runs git to list the files again.
//
// A Watcher does not see a change of the global core.excludesFile. It
// also does not see a copy that keeps the size and the modification time
// of a file.
type Watcher struct {
	r              *Repo
	gitDir, common string           // absolute paths
	now            func() time.Time // the clock, time.Now
	next           time.Time        // no check before this time

	started bool
	since   time.Time // the start of the watch
	listed  bool      // files and dirs are from the last list
	files   []string  // the absolute paths that git lists
	dirs    []string  // the root and each directory above a listed path
	shown   []string  // the absolute paths of the ignored dirs that show

	// The sums of the last check.
	list, refs, index, dirSum, fileSum, shownSum uint64
}

// Change tells what can have changed since the last call to
// [Watcher.Changed].
type Change struct {
	Files bool // the working tree or the index
	Refs  bool // HEAD, a ref, or the config
}

// Watch returns a Watcher of the repository. Its first call to
// [Watcher.Changed] reports a change of both.
func (r *Repo) Watch(ctx context.Context) (*Watcher, error) {
	gitDir, err := r.gitPath(ctx, "--git-dir")
	if err != nil {
		return nil, err
	}
	common, err := r.gitPath(ctx, "--git-common-dir")
	if err != nil {
		return nil, err
	}
	w := &Watcher{r: r, gitDir: gitDir, common: common, now: time.Now, since: time.Now()}
	// The list is ready at once, for LastWrite.
	w.index = w.sumIndex()
	if w.list, err = w.relist(ctx); err != nil {
		return nil, err
	}
	w.dirSum = sumPaths(w.dirs)
	return w, nil
}

// Show sets the paths that show and that git does not list. These are the
// ignored directories that the user expands, and the open file. The open
// file can be ignored or a link. paths are relative to the root. Changed
// also reports an edit of a shown file or of the target of a shown link.
// It also reports a file that a shown directory gets or loses. A new set
// reports no change.
func (w *Watcher) Show(paths []string) {
	abs := make([]string, len(paths))
	for i, p := range paths {
		abs[i] = filepath.Join(w.r.root, filepath.FromSlash(p))
	}
	if !slices.Equal(abs, w.shown) {
		w.shown, w.shownSum = abs, sumPaths(abs)
	}
}

// gitPath returns the absolute path that rev-parse gives for flag.
func (r *Repo) gitPath(ctx context.Context, flag string) (string, error) {
	out, err := r.run(ctx, nil, "rev-parse", "--path-format=absolute", flag)
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(out, "\n"), nil
}

// Changed tells what can have changed since the last call. The first call
// reports a change of both. After a call that takes the time d, Changed
// reports no change for 20 times d. A change in that time shows in the
// next call after it.
func (w *Watcher) Changed(ctx context.Context) (Change, error) {
	start := w.now()
	if start.Before(w.next) {
		return Change{}, nil
	}
	defer func() {
		end := w.now()
		w.next = end.Add(pauseFactor * end.Sub(start))
	}()

	refs, index := w.sumRefs(), w.sumIndex()
	dirs := sumPaths(w.dirs)
	listed := false
	list := w.list
	if !w.listed || dirs != w.dirSum || index != w.index {
		var err error
		if list, err = w.relist(ctx); err != nil {
			return Change{}, err
		}
		dirs, listed = sumPaths(w.dirs), true
	}
	files, shown := sumFiles(w.files), sumPaths(w.shown)
	ch := Change{
		Files: index != w.index || list != w.list || files != w.fileSum,
		Refs:  refs != w.refs,
	}
	if ch.Files && !listed {
		// An edit of a .gitignore file can change the list.
		var err error
		if list, err = w.relist(ctx); err != nil {
			return Change{}, err
		}
		dirs, files = sumPaths(w.dirs), sumFiles(w.files)
	}
	if !w.started {
		w.started, ch = true, Change{Files: true, Refs: true}
	}
	w.list, w.refs, w.index, w.dirSum, w.fileSum = list, refs, index, dirs, files
	if shown != w.shownSum {
		w.shownSum, ch.Files = shown, true
	}
	return ch, nil
}

// LastWrite returns the newest modification time of the listed files and
// of their directories, as they are now. A new or a removed file changes
// the time of its directory. LastWrite returns the zero time if no such
// time is after the start of the watch. It does not change what the next
// call to Changed reports.
func (w *Watcher) LastWrite() time.Time {
	var last time.Time
	for _, p := range slices.Concat(w.files, w.dirs) {
		if fi, err := os.Lstat(p); err == nil && fi.ModTime().After(last) {
			last = fi.ModTime()
		}
	}
	if !last.After(w.since) {
		return time.Time{}
	}
	return last
}

// relist reads the list of the files and their directories again. It
// returns the sum of the list.
func (w *Watcher) relist(ctx context.Context) (uint64, error) {
	w.listed = false
	paths, err := w.r.lsFiles(ctx, listArgs...)
	if err != nil {
		return 0, err
	}
	untracked, err := w.r.untrackedDirs(ctx)
	if err != nil {
		return 0, err
	}
	root := w.r.root
	seen := map[string]bool{root: true}
	addDirs := func(p string) {
		for d := p; !seen[d]; d = filepath.Dir(d) {
			seen[d] = true
			w.dirs = append(w.dirs, d)
		}
	}
	w.dirs = append(w.dirs[:0], root)
	w.files = w.files[:0]
	s := newSum()
	for _, p := range paths {
		s.str(p)
		// git lists a nested repository with a slash at the end.
		p = filepath.Join(root, strings.TrimSuffix(p, "/"))
		w.files = append(w.files, p)
		addDirs(filepath.Dir(p))
	}
	for _, d := range untracked {
		addDirs(filepath.Join(root, d))
	}
	w.listed = true
	return s.Sum64(), nil
}

// untrackedDirs returns the untracked directories, relative to the root,
// and each directory below them that git does not ignore. git lists no
// file in a directory that is empty or has only ignored files. A new file
// there changes only that directory, thus the [Watcher] must watch it.
// untrackedDirs does not go into a nested repository.
func (r *Repo) untrackedDirs(ctx context.Context) ([]string, error) {
	ps, err := r.lsFiles(ctx, "-o", "--directory", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	var level, dirs []string
	for _, p := range ps {
		if d, ok := strings.CutSuffix(p, "/"); ok {
			level = append(level, d)
		}
	}
	for len(level) > 0 {
		dirs = append(dirs, level...)
		var next []string
		for _, d := range level {
			abs := filepath.Join(r.root, filepath.FromSlash(d))
			if _, err := os.Lstat(filepath.Join(abs, ".git")); err == nil {
				continue // a nested repository
			}
			// A directory that goes away has no directories to watch.
			des, _ := os.ReadDir(abs)
			for _, de := range des {
				if de.IsDir() {
					next = append(next, d+"/"+de.Name())
				}
			}
		}
		if level, err = r.notIgnored(ctx, next); err != nil {
			return nil, err
		}
	}
	return dirs, nil
}

// notIgnored returns the paths that git does not ignore. Each path gets
// the prefix "./", because git reads a path that starts with ":" as
// pathspec magic. git writes the paths with the prefix.
func (r *Repo) notIgnored(ctx context.Context, paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	in := strings.NewReader("./" + strings.Join(paths, "\x00./"))
	out, err := r.runInput(ctx, nil, in, "check-ignore", "-z", "--stdin")
	if exitedOne(err) {
		return paths, nil // git ignores none
	}
	if err != nil {
		return nil, err
	}
	ignored := map[string]bool{}
	for p := range strings.SplitSeq(out, "\x00") {
		ignored[p] = true
	}
	return slices.DeleteFunc(paths, func(p string) bool { return ignored["./"+p] }), nil
}

// sumRefs returns the sum of the files that change with HEAD, a ref, or
// the config.
func (w *Watcher) sumRefs() uint64 {
	paths := []string{
		filepath.Join(w.gitDir, "HEAD"),
		filepath.Join(w.gitDir, "logs", "HEAD"),
		filepath.Join(w.common, "packed-refs"),
		filepath.Join(w.common, "FETCH_HEAD"),
		filepath.Join(w.common, "config"),
		filepath.Join(w.common, "reftable", "tables.list"),
	}
	// The branch of HEAD has its own file. The branches are in the common
	// directory, which the worktrees of the repository share.
	if b, err := os.ReadFile(paths[0]); err == nil {
		ref, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "ref: ")
		// Localize refuses a ".." or a "/" at the start.
		if p, err := filepath.Localize(ref); ok && err == nil {
			paths = append(paths, filepath.Join(w.common, p))
		}
	}
	return sumPaths(paths)
}

// sumIndex returns the sum of the files that change the list of files.
func (w *Watcher) sumIndex() uint64 {
	return sumPaths([]string{w.r.index, filepath.Join(w.common, "info", "exclude")})
}

// sumPaths returns the sum of the path, the size, the modification time,
// the mode, and the inode of each file. It follows a symbolic link, so an
// edit of the target changes the sum.
func sumPaths(paths []string) uint64 {
	s := newSum()
	for _, p := range paths {
		s.stat(p, os.Stat)
	}
	return s.Sum64()
}

// sumFiles is like sumPaths. For a directory, it also adds the git files
// of the nested repository or the submodule in it. A commit there does not
// change the directory.
func sumFiles(paths []string) uint64 {
	s := newSum()
	for _, p := range paths {
		if s.stat(p, os.Lstat) {
			s.nested(p)
		}
	}
	return s.Sum64()
}

// sum is a 64-bit FNV-1a hash of paths and of the states of files. The
// sums stay in the process, so their values can change between versions.
// The Write of a hash never fails.
type sum struct{ hash.Hash64 }

// newSum returns the sum of no bytes.
func newSum() sum { return sum{fnv.New64a()} }

// str adds v and a NUL, so that two strings do not join.
func (s sum) str(v string) { _, _ = s.Write(append([]byte(v), 0)) }

// num adds the 8 bytes of v.
func (s sum) num(v uint64) { _, _ = s.Write(binary.LittleEndian.AppendUint64(nil, v)) }

// int adds v, for example a size or a time.
func (s sum) int(v int64) {
	s.num(uint64(v)) //nolint:gosec // a hash keeps all bits, so a wrap is fine
}

// stat adds the path and the file status that stat gives for p. A missing
// file adds a marker. stat reports whether p is a directory.
func (s sum) stat(p string, stat func(string) (fs.FileInfo, error)) (isDir bool) {
	s.str(p)
	fi, err := stat(p)
	if err != nil {
		s.num(0)
		return false
	}
	s.int(fi.Size())
	s.int(fi.ModTime().UnixNano())
	s.num(uint64(fi.Mode()))
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		s.num(st.Ino)
	}
	return fi.IsDir()
}

// nested adds the git files of the repository in the directory p, if p
// has one. A commit there changes HEAD or its log, and the directory of
// the git files.
func (s sum) nested(p string) {
	dotGit := filepath.Join(p, ".git")
	fi, err := os.Lstat(dotGit)
	if err != nil {
		return
	}
	s.str(dotGit)
	dir := dotGit
	if !fi.IsDir() {
		// A submodule has a file "gitdir: <path>" in place of a directory.
		b, err := os.ReadFile(dotGit)
		gd, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir: ")
		if err != nil || !ok {
			return
		}
		if dir = gd; !filepath.IsAbs(dir) {
			dir = filepath.Join(p, dir)
		}
	}
	for _, f := range []string{dir, filepath.Join(dir, "HEAD"), filepath.Join(dir, "logs", "HEAD")} {
		fi, err := os.Lstat(f)
		if err != nil {
			s.num(0)
			continue
		}
		s.int(fi.Size())
		s.int(fi.ModTime().UnixNano())
	}
}

// lsFiles runs git ls-files with args and returns the listed paths.
func (r *Repo) lsFiles(ctx context.Context, args ...string) ([]string, error) {
	out, err := r.run(ctx, nil, slices.Concat([]string{"ls-files", "-z"}, args)...)
	if err != nil {
		return nil, err
	}
	var ps []string
	for p := range strings.SplitSeq(out, "\x00") {
		if p != "" {
			ps = append(ps, p)
		}
	}
	return ps, nil
}
