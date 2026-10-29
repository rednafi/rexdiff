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
	"sync"
	"syscall"
	"time"
)

// Pauses between the checks of a [Watcher]. After a check, the next one
// waits pauseFactor times as long as the check took, so checking uses less
// than 1% of a CPU core, but never more than maxPause. The time spent
// listing the files with git does not count: listing happens only after
// a change, and a pause after it would delay noticing the next edit by
// seconds in a large repository.
const (
	pauseFactor = 100
	maxPause    = 30 * time.Second
)

// Watcher reports whether the working tree or the refs may have changed.
// It compares the file metadata of the files that git lists, their
// directories and git's own files. While nothing changes, it runs no git
// process. After a change, it runs git to list the files again.
//
// A Watcher does not notice changes to the global core.excludesFile, or a
// file replaced by a copy with the same size and modification time.
//
// A Watcher is safe for concurrent use by multiple goroutines.
type Watcher struct {
	mu             sync.Mutex // held by every method
	r              *Repo
	gitDir, common string           // absolute paths
	now            func() time.Time // the clock; time.Now outside tests
	next           time.Time        // the earliest time of the next check

	started  bool
	reset    bool      // the next check reports a change to the files
	since    time.Time // when the watch started
	listed   bool      // files and dirs come from a complete listing
	files    []string  // the absolute paths that git lists
	dirs     []string  // the root and every directory that contains a listed path
	included []string  // the absolute paths set by Include

	// The sums from the last check.
	list, refs, index, dirSum, fileSum, includedSum uint64
}

// Change describes what may have changed since the last call to
// [Watcher.Changed].
type Change struct {
	Files bool // the working tree or the index may have changed
	Refs  bool // HEAD, a ref or the git config may have changed
}

// Watch returns a Watcher for the repository. The first call to
// [Watcher.Changed] reports both kinds of change.
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
	// List the files now, so LastWrite works before the first call to
	// Changed.
	w.index = w.sumIndex()
	if w.list, err = w.relist(ctx); err != nil {
		return nil, err
	}
	// Sum the files now too, or else the first call to Changed would see
	// them change and list them again.
	w.dirSum, w.fileSum = sumPaths(w.dirs), sumFiles(w.files)
	return w, nil
}

// Include sets extra paths to watch that git does not list, for example
// expanded ignored directories and the file that is open. paths are
// relative to the root. Changed then also reports edits to such a file or
// to its symbolic link target, and files added to or removed from such a
// directory. Calling Include does not itself count as a change.
func (w *Watcher) Include(paths []string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	abs := make([]string, len(paths))
	for i, p := range paths {
		abs[i] = filepath.Join(w.r.root, filepath.FromSlash(p))
	}
	if !slices.Equal(abs, w.included) {
		w.included, w.includedSum = abs, sumPaths(abs)
	}
}

// Reset makes the next call to [Watcher.Changed] check at once and report
// a change to the files. Call it after reading the files without a
// reported change, for example on a key press. Then the reread that follows
// that report starts after the check, and the checks compare with what is
// shown.
func (w *Watcher) Reset() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.reset, w.next = true, time.Time{}
}

// gitPath returns the absolute path that git rev-parse prints for flag.
func (r *Repo) gitPath(ctx context.Context, flag string) (string, error) {
	out, err := r.run(ctx, nil, "rev-parse", "--path-format=absolute", flag)
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(out, "\n"), nil
}

// Changed reports what may have changed since the last call. The first
// call reports both kinds of change. If a call takes the time d, not
// counting the time spent listing files with git, calls in the following
// 100 times d, but at most 30 seconds, report no change without checking.
// The first call after that period reports a change made in it.
func (w *Watcher) Changed(ctx context.Context) (Change, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	start := w.now()
	if start.Before(w.next) {
		return Change{}, nil
	}
	var listing time.Duration // the time spent in relist
	relist := func() (uint64, error) {
		t0 := w.now()
		defer func() { listing += w.now().Sub(t0) }()
		return w.relist(ctx)
	}
	defer func() {
		end := w.now()
		w.next = end.Add(min(pauseFactor*(end.Sub(start)-listing), maxPause))
	}()

	refs, index := w.sumRefs(), w.sumIndex()
	dirs := sumPaths(w.dirs)
	listed := false
	list := w.list
	if !w.listed || dirs != w.dirSum || index != w.index {
		var err error
		if list, err = relist(); err != nil {
			return Change{}, err
		}
		dirs, listed = sumPaths(w.dirs), true
	}
	files, included := sumFiles(w.files), sumPaths(w.included)
	ch := Change{
		Files: w.reset || index != w.index || list != w.list || files != w.fileSum,
		Refs:  refs != w.refs,
	}
	if ch.Files && !listed {
		// Editing a .gitignore file can change which files git lists.
		var err error
		if list, err = relist(); err != nil {
			return Change{}, err
		}
		dirs, files = sumPaths(w.dirs), sumFiles(w.files)
	}
	if !w.started {
		w.started, ch = true, Change{Files: true, Refs: true}
	}
	w.list, w.refs, w.index, w.dirSum, w.fileSum = list, refs, index, dirs, files
	w.reset = false
	if included != w.includedSum {
		w.includedSum, ch.Files = included, true
	}
	return ch, nil
}

// LastWrite returns the latest modification time, no later than until, of
// the listed files and their directories. Adding or removing a file
// updates the time of its directory. If no such time is after the watch
// started, LastWrite returns the zero time. It does not affect what the
// next call to Changed reports.
func (w *Watcher) LastWrite(until time.Time) time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	var last time.Time
	for _, p := range slices.Concat(w.files, w.dirs) {
		info, err := os.Lstat(p)
		if err == nil && info.ModTime().After(last) && !info.ModTime().After(until) {
			last = info.ModTime()
		}
	}
	if !last.After(w.since) {
		return time.Time{}
	}
	return last
}

// relist lists the files and their directories again and returns a hash
// of the file list.
func (w *Watcher) relist(ctx context.Context) (uint64, error) {
	w.listed = false
	paths, err := w.r.lsFiles(ctx, lsFilesArgs...)
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
		// git lists a nested repository with a trailing slash.
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
// and every directory below them that git does not ignore. The [Watcher]
// needs them because git lists no files for a directory that is empty or
// holds only ignored files. untrackedDirs does not descend into nested
// repositories.
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
				continue // skip a nested repository
			}
			// If the directory was just deleted, ReadDir fails and there is
			// nothing below it to watch.
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

// notIgnored returns the paths that git does not ignore. It prefixes each
// path with "./" before passing it to git, because git treats a leading
// ":" as pathspec magic.
func (r *Repo) notIgnored(ctx context.Context, paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	in := strings.NewReader("./" + strings.Join(paths, "\x00./"))
	out, err := r.runInput(ctx, nil, in, "check-ignore", "-z", "--stdin")
	if isExitOne(err) {
		return paths, nil // git ignores none of the paths
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

// sumRefs returns a hash of the files that change when HEAD, a ref or the
// git config changes.
func (w *Watcher) sumRefs() uint64 {
	paths := []string{
		filepath.Join(w.gitDir, "HEAD"),
		filepath.Join(w.gitDir, "logs", "HEAD"),
		filepath.Join(w.common, "packed-refs"),
		filepath.Join(w.common, "FETCH_HEAD"),
		filepath.Join(w.common, "config"),
		filepath.Join(w.common, "reftable", "tables.list"),
	}
	// The branch that HEAD points to has its own file, in the common
	// directory that all worktrees share.
	if b, err := os.ReadFile(paths[0]); err == nil {
		ref, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "ref: ")
		// Localize rejects paths with ".." or a leading "/".
		if p, err := filepath.Localize(ref); ok && err == nil {
			paths = append(paths, filepath.Join(w.common, p))
		}
	}
	return sumPaths(paths)
}

// sumIndex returns a hash of the files that affect which files git lists:
// the index and info/exclude.
func (w *Watcher) sumIndex() uint64 {
	return sumPaths([]string{w.r.index, filepath.Join(w.common, "info", "exclude")})
}

// sumPaths returns a hash of the path, size, modification time, mode and
// inode of each file. It follows symbolic links, so editing a link's
// target changes the hash.
func sumPaths(paths []string) uint64 {
	s := newSum()
	for _, p := range paths {
		s.file(p, statOf(os.Stat, p))
	}
	return s.Sum64()
}

// sumFiles is like sumPaths, but it does not follow symbolic links. For a
// directory that holds a nested repository or submodule, it also hashes
// that repository's git files, because a commit there does not change the
// directory itself.
func sumFiles(paths []string) uint64 {
	s := newSum()
	for _, p := range paths {
		if s.file(p, statOf(os.Lstat, p)) {
			s.nested(p)
		}
	}
	return s.Sum64()
}

// statOf returns the result of calling stat on p, or nil if that fails,
// for example because p does not exist.
func statOf(stat func(string) (fs.FileInfo, error), p string) fs.FileInfo {
	info, err := stat(p)
	if err != nil {
		return nil
	}
	return info
}

// sum is a 64-bit FNV-1a hash of paths and file metadata. Sums are never
// stored outside the process, so the hashed format can change between
// versions. Write never fails. buf is reused for every write to avoid an
// allocation.
type sum struct {
	hash.Hash64
	buf []byte
}

// newSum returns an empty sum.
func newSum() *sum { return &sum{Hash64: fnv.New64a()} }

// str adds v followed by a NUL byte, so that adjacent strings stay
// distinct.
func (s *sum) str(v string) {
	s.buf = append(append(s.buf[:0], v...), 0)
	_, _ = s.Write(s.buf)
}

// num adds v as 8 bytes.
func (s *sum) num(v uint64) {
	s.buf = binary.LittleEndian.AppendUint64(s.buf[:0], v)
	_, _ = s.Write(s.buf)
}

// int adds v, for example a size or a time.
func (s *sum) int(v int64) {
	s.num(uint64(v)) //nolint:gosec // the hash uses all 64 bits, so the conversion loses nothing
}

// file adds the path p and its metadata info. A nil info means that the
// file does not exist. file reports whether p is a directory.
func (s *sum) file(p string, info fs.FileInfo) (isDir bool) {
	s.str(p)
	if info == nil {
		s.num(0)
		return false
	}
	s.int(info.Size())
	s.int(info.ModTime().UnixNano())
	s.num(uint64(info.Mode()))
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		s.num(st.Ino)
	}
	return info.IsDir()
}

// nested adds the git directory, HEAD and HEAD log of the repository in
// the directory p, if p contains one.
func (s *sum) nested(p string) {
	dotGit := filepath.Join(p, ".git")
	info, err := os.Lstat(dotGit)
	if err != nil {
		return
	}
	s.str(dotGit)
	dir := dotGit
	if !info.IsDir() {
		// In a submodule, .git is a file containing "gitdir: <path>"
		// instead of a directory.
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
		info, err := os.Lstat(f)
		if err != nil {
			s.num(0)
			continue
		}
		s.int(info.Size())
		s.int(info.ModTime().UnixNano())
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
