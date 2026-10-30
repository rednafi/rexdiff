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

// The next check waits pauseFactor times as long as the last check took,
// up to maxPause. The time to list files does not count.
const (
	pauseFactor = 100
	maxPause    = 30 * time.Second
)

// Watcher reports whether the working tree or the refs may have changed.
// It does not see changes to the global core.excludesFile. A Watcher is
// safe for concurrent use.
type Watcher struct {
	mu             sync.Mutex
	r              *Repo
	gitDir, common string
	now            func() time.Time
	next           time.Time

	started  bool
	reset    bool // the next check reports a change to the files
	since    time.Time
	listed   bool // files and dirs come from a complete listing
	files    []string
	dirs     []string // the root and every directory of a listed path
	included []string

	list, refs, index, dirSum, fileSum, includedSum uint64
}

// Change describes what may have changed.
type Change struct {
	Files bool // the working tree or the index may have changed
	Refs  bool // HEAD, a ref or the git config may have changed
}

// Watch returns a Watcher for the repository.
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
	// List the files now, so LastWrite works before the first Changed call.
	w.index = w.sumIndex()
	if w.list, err = w.relist(ctx); err != nil {
		return nil, err
	}
	// Sum them now too, so the first Changed call does not list them again.
	w.dirSum, w.fileSum = sumPaths(w.dirs), sumFiles(w.files)
	return w, nil
}

// Include sets extra paths that git does not list. It is not a change.
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

// Reset makes the next [Watcher.Changed] call check now and report Files.
func (w *Watcher) Reset() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.reset, w.next = true, time.Time{}
}

func (r *Repo) gitPath(ctx context.Context, flag string) (string, error) {
	out, err := r.run(ctx, nil, "rev-parse", "--path-format=absolute", flag)
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(out, "\n"), nil
}

// Changed reports what may have changed since the last call. The first
// call reports both kinds. After a call that takes d, calls in the next
// 100*d report no change.
func (w *Watcher) Changed(ctx context.Context) (Change, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	start := w.now()
	if start.Before(w.next) {
		return Change{}, nil
	}
	var listing time.Duration
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

// LastWrite returns the latest mtime, up to until, of the listed paths. It
// is zero if that is not after the watch started.
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

// untrackedDirs is necessary because git lists no files for an empty
// directory or one with only ignored files.
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
				continue
			}
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

// notIgnored prefixes each path with "./", because git reads a leading ":"
// as pathspec magic.
func (r *Repo) notIgnored(ctx context.Context, paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	in := strings.NewReader("./" + strings.Join(paths, "\x00./"))
	out, err := r.runInput(ctx, nil, in, "check-ignore", "-z", "--stdin")
	if isExitOne(err) {
		return paths, nil // none are ignored
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

func (w *Watcher) sumRefs() uint64 {
	paths := []string{
		filepath.Join(w.gitDir, "HEAD"),
		filepath.Join(w.gitDir, "logs", "HEAD"),
		filepath.Join(w.common, "packed-refs"),
		filepath.Join(w.common, "FETCH_HEAD"),
		filepath.Join(w.common, "config"),
		filepath.Join(w.common, "reftable", "tables.list"),
	}
	// The branch of HEAD has its own file in the shared common directory.
	if b, err := os.ReadFile(paths[0]); err == nil {
		ref, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "ref: ")
		// Localize rejects paths with ".." or a leading "/".
		if p, err := filepath.Localize(ref); ok && err == nil {
			paths = append(paths, filepath.Join(w.common, p))
		}
	}
	return sumPaths(paths)
}

func (w *Watcher) sumIndex() uint64 {
	return sumPaths([]string{w.r.index, filepath.Join(w.common, "info", "exclude")})
}

// sumPaths follows symbolic links, so an edit to a target changes the sum.
func sumPaths(paths []string) uint64 {
	s := newSum()
	for _, p := range paths {
		s.file(p, statOf(os.Stat, p))
	}
	return s.Sum64()
}

// sumFiles does not follow symbolic links. It also hashes the git files of
// a nested repository, because a commit there does not change the directory.
func sumFiles(paths []string) uint64 {
	s := newSum()
	for _, p := range paths {
		if s.file(p, statOf(os.Lstat, p)) {
			s.nested(p)
		}
	}
	return s.Sum64()
}

func statOf(stat func(string) (fs.FileInfo, error), p string) fs.FileInfo {
	info, err := stat(p)
	if err != nil {
		return nil
	}
	return info
}

type sum struct {
	hash.Hash64
	buf []byte
}

func newSum() *sum { return &sum{Hash64: fnv.New64a()} }

// str adds a NUL byte after v, so adjacent strings stay distinct.
func (s *sum) str(v string) {
	s.buf = append(append(s.buf[:0], v...), 0)
	_, _ = s.Write(s.buf)
}

func (s *sum) num(v uint64) {
	s.buf = binary.LittleEndian.AppendUint64(s.buf[:0], v)
	_, _ = s.Write(s.buf)
}

func (s *sum) int(v int64) {
	s.num(uint64(v)) //nolint:gosec // the hash uses all 64 bits, so the conversion loses nothing
}

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

func (s *sum) nested(p string) {
	dotGit := filepath.Join(p, ".git")
	info, err := os.Lstat(dotGit)
	if err != nil {
		return
	}
	s.str(dotGit)
	dir := dotGit
	if !info.IsDir() {
		// In a submodule, .git is a file that contains "gitdir: <path>".
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
