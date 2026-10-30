// Package vcs reads the changes in a git repository and finds the pull
// request for its current branch.
package vcs

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rednafi/rexdiff/internal/proc"
)

const emptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

const (
	maxFileSize = 2_000_000 // bytes
	maxLines    = 50_000
)

// Limits for a whole diff, so a large change cannot fill the memory.
const (
	maxDiffBytes    = 128 << 20 // old and new text together
	maxChangedFiles = 20_000
)

var (
	// ErrBinary means that a file is binary.
	ErrBinary = errors.New("binary file")
	// ErrTooBig means that a file has too many bytes or lines.
	ErrTooBig     = errors.New("file too large")
	errDiffTooBig = fmt.Errorf("changes too large: %w", ErrTooBig)
	errNoCommits  = errors.New("no commits yet")
)

// Repo is a git repository.
type Repo struct {
	root  string
	index string
	env   []string

	// snapLock guards the private index and the fields below. It is a
	// channel, so a waiting caller can stop when its ctx ends.
	snapLock chan struct{}
	snapDir  string // "" before the first snapshot
	snapSeed string // size and mtime of the real index at the last copy
	closed   bool

	// blobs caches the objects that the last Diff read, by object ID.
	blobMu sync.Mutex
	blobs  map[string]blob

	maxDiffBytes int // tests lower it
}

var errClosed = errors.New("the repository is closed")

// Open returns the repository that contains dir.
func Open(ctx context.Context, env []string, dir string) (*Repo, error) {
	// git receives the root directory in PWD, which must be absolute.
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	r := &Repo{
		root: abs, env: env, snapLock: make(chan struct{}, 1),
		maxDiffBytes: maxDiffBytes,
	}
	// A path can contain a newline or end with a space. So use one git
	// command per path and remove only the final newline.
	root, err := r.run(ctx, nil, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, err
	}
	// Resolve symbolic links once, so Contains and Owns compare real paths.
	r.root = evalSymlinks(strings.TrimSuffix(root, "\n"))
	index, err := r.run(ctx, nil,
		"rev-parse", "--path-format=absolute", "--git-path", "index")
	if err != nil {
		return nil, err
	}
	r.index = strings.TrimSuffix(index, "\n")
	return r, nil
}

// Close removes the temporary files. It waits for a running SnapshotTree.
func (r *Repo) Close() error {
	r.snapLock <- struct{}{}
	defer func() { <-r.snapLock }()
	dir := r.snapDir
	r.snapDir, r.closed = "", true
	if dir == "" {
		return nil
	}
	return os.RemoveAll(dir)
}

// Root returns the top directory of the working tree.
func (r *Repo) Root() string { return r.root }

func (r *Repo) run(ctx context.Context, env []string, args ...string) (string, error) {
	return r.runInput(ctx, env, nil, args...)
}

func (r *Repo) runInput(ctx context.Context, env []string, in io.Reader, args ...string) (string, error) {
	cmd, stderr := r.command(ctx, env, "git", args...)
	cmd.Stdin = in
	out, err := output(ctx, cmd, stderr)
	if err != nil {
		return out, fmt.Errorf("git %s: %w", args[0], err)
	}
	return out, nil
}

func (r *Repo) command(ctx context.Context, env []string, name string, args ...string) (*exec.Cmd, *bytes.Buffer) {
	// Set PWD, as exec does when Env is nil. Concat does not modify r.env.
	env = slices.Concat(r.env, []string{"PWD=" + r.root}, env)
	cmd := proc.Command(ctx, env, name, args...)
	cmd.Dir = r.root
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	return cmd, &stderr
}

func output(ctx context.Context, cmd *exec.Cmd, stderr *bytes.Buffer) (string, error) {
	out, err := cmd.Output()
	if err != nil {
		return string(out), proc.Error(ctx, cmd.Args[0], err, stderr.Bytes())
	}
	return string(out), nil
}

func isExitOne(err error) bool {
	ee, ok := errors.AsType[*exec.ExitError](err)
	return ok && ee.ExitCode() == 1
}

// ResolveRev returns the object ID of rev, for example "HEAD" or "main^{tree}".
func (r *Repo) ResolveRev(ctx context.Context, rev string) (string, error) {
	out, err := r.run(ctx, nil, "rev-parse", "--verify", "--quiet", rev)
	if isExitOne(err) {
		return "", fmt.Errorf("git: unknown revision %q", rev)
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// Tree returns the tree ID of the commit rev.
func (r *Repo) Tree(ctx context.Context, rev string) (string, error) {
	return r.ResolveRev(ctx, rev+"^{tree}")
}

// HeadTree returns the tree ID of HEAD, or the empty tree if it has none.
func (r *Repo) HeadTree(ctx context.Context) (string, error) {
	t, err := r.Tree(ctx, "HEAD")
	if err == nil {
		return t, nil
	}
	// rev-parse exits 1 only if HEAD has no commit yet, not if it is broken.
	_, rerr := r.run(ctx, nil, "rev-parse", "-q", "--verify", "HEAD")
	if isExitOne(rerr) {
		return emptyTree, nil
	}
	return "", err
}

// ParentTree returns the tree of the first parent of rev, or the empty tree.
func (r *Repo) ParentTree(ctx context.Context, rev string) (string, error) {
	c, err := r.ResolveRev(ctx, rev+"^{commit}")
	if err != nil {
		return "", err
	}
	out, err := r.run(ctx, nil, "rev-parse", c+"^@")
	if err != nil {
		return "", err
	}
	first, _, _ := strings.Cut(out, "\n")
	if first == "" {
		return emptyTree, nil
	}
	return r.Tree(ctx, first)
}

// MergeBaseTree returns the tree ID of [Repo.MergeBaseCommit].
func (r *Repo) MergeBaseTree(ctx context.Context, base string) (string, error) {
	c, err := r.MergeBaseCommit(ctx, base)
	if err != nil {
		return "", err
	}
	return r.Tree(ctx, c)
}

// MergeBaseCommit returns the merge base commit of base and HEAD. Other
// git errors wrap the [*exec.ExitError] of git.
func (r *Repo) MergeBaseCommit(ctx context.Context, base string) (string, error) {
	out, err := r.run(ctx, nil, "merge-base", "--end-of-options", base, "HEAD")
	if err != nil {
		if _, rerr := r.run(ctx, nil, "rev-parse", "-q", "--verify", "HEAD"); isExitOne(rerr) {
			return "", errNoCommits
		}
		return "", fmt.Errorf("no merge base with %s: %w", base, err)
	}
	return strings.TrimSpace(out), nil
}

// SnapshotTree writes the working tree as a tree object and returns its ID.
// It does not change the real index.
func (r *Repo) SnapshotTree(ctx context.Context) (string, error) {
	select {
	case r.snapLock <- struct{}{}:
	case <-ctx.Done():
		return "", context.Cause(ctx)
	}
	defer func() { <-r.snapLock }()
	if r.closed {
		return "", errClosed
	}
	env, err := r.privateIndex()
	if err != nil {
		return "", fmt.Errorf("snapshot: %w", err)
	}
	if err := r.removeIgnored(ctx, env); err != nil {
		return "", fmt.Errorf("snapshot: %w", err)
	}
	_, err = r.run(ctx, env, "add", "-A", "--ignore-errors")
	if isExitOne(err) {
		err = nil // git could not add some files, but it added the others
	}
	if err != nil {
		return "", fmt.Errorf("snapshot: %w", err)
	}
	out, err := r.run(ctx, env, "write-tree")
	if err != nil {
		return "", fmt.Errorf("snapshot: %w", err)
	}
	return strings.TrimSpace(out), nil
}

// privateIndex copies the real index again when it changes. The caller
// must hold snapLock.
func (r *Repo) privateIndex() ([]string, error) {
	seed := ""
	if info, err := os.Stat(r.index); err == nil {
		seed = fmt.Sprint(info.Size(), info.ModTime().UnixNano())
	}
	_, err := os.Stat(r.snapDir)
	if r.snapDir == "" || err != nil || seed != r.snapSeed {
		if r.snapDir != "" {
			_ = os.RemoveAll(r.snapDir)
		}
		dir, err := os.MkdirTemp(proc.Getenv(r.env, "TMPDIR"), "rexdiff-index-*")
		if err != nil {
			return nil, err
		}
		if err := r.copyIndex(filepath.Join(dir, "index")); err != nil {
			_ = os.RemoveAll(dir)
			return nil, err
		}
		r.snapDir, r.snapSeed = dir, seed
	}
	return []string{"GIT_INDEX_FILE=" + filepath.Join(r.snapDir, "index")}, nil
}

// removeIgnored is necessary, because "git add -A" keeps newly ignored files.
func (r *Repo) removeIgnored(ctx context.Context, env []string) error {
	ignored := []string{"ls-files", "-z", "-c", "-i", "--exclude-standard"}
	out, err := r.run(ctx, env, ignored...)
	if err != nil || out == "" {
		return err
	}
	tracked, err := r.run(ctx, nil, ignored...) // keep files added with "git add -f"
	if err != nil {
		return err
	}
	keep := map[string]bool{}
	for p := range strings.SplitSeq(tracked, "\x00") {
		keep[p] = true
	}
	var removed strings.Builder
	for p := range strings.SplitSeq(strings.TrimSuffix(out, "\x00"), "\x00") {
		if !keep[p] {
			removed.WriteString(p)
			removed.WriteString("\x00")
		}
	}
	if removed.Len() == 0 {
		return nil
	}
	_, err = r.runInput(ctx, env, strings.NewReader(removed.String()),
		"rm", "--cached", "-q", "--ignore-unmatch", "--pathspec-from-file=-", "--pathspec-file-nul")
	return err
}

// copyIndex keeps the modification time of the original. Git uses it to
// find files that changed in the same second as the index was written.
func (r *Repo) copyIndex(dst string) error {
	info, err := os.Stat(r.index)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	b, err := os.ReadFile(r.index)
	if err != nil {
		return err
	}
	if err := os.WriteFile(dst, b, 0o600); err != nil {
		return err
	}
	return os.Chtimes(dst, time.Time{}, info.ModTime())
}

// FileStatus is the git status letter of a change, for example 'A'.
type FileStatus byte

// The values of FileStatus that [Repo.Diff] returns.
const (
	StatusAdded    FileStatus = 'A'
	StatusModified FileStatus = 'M'
	StatusDeleted  FileStatus = 'D'
	StatusRenamed  FileStatus = 'R'

	statusTypeChange FileStatus = 'T'
)

// File is a file that differs between two trees. For a submodule, each
// version is the line "Subproject commit <id>".
type File struct {
	Path             string
	OldPath          string // set only for a rename
	Status           FileStatus
	OldText, NewText string
	// Err is ErrBinary, or wraps ErrTooBig, when the texts are empty.
	Err error
}

const gitlink = "160000"

// TooManyFilesError means that too many files differ.
type TooManyFilesError struct {
	Count int
}

// Error returns the number of changed files.
func (e *TooManyFilesError) Error() string {
	return fmt.Sprintf("%d files changed, too many to show", e.Count)
}

// Diff returns the files that differ between oldTree and newTree, sorted
// by path. If too many files differ, it returns a [*TooManyFilesError].
func (r *Repo) Diff(ctx context.Context, oldTree, newTree string) ([]File, error) {
	out, err := r.run(ctx, nil,
		"diff", "--raw", "-z", "--no-abbrev", "--find-renames",
		"--end-of-options", oldTree, newTree)
	if err != nil {
		return nil, err
	}
	// Each file is ":<old mode> <new mode> <old id> <new id> <status>"
	// followed by its path. A rename is followed by the old and new paths.
	fields := strings.Split(strings.TrimSuffix(out, "\x00"), "\x00")
	var files []File
	var ids []string // old and new object ID of each file, or ""
	var blobs []blob
	for len(fields) >= 2 {
		meta := strings.Fields(strings.TrimPrefix(fields[0], ":"))
		if len(meta) != 5 {
			return nil, fmt.Errorf("git diff: bad output %q", fields[0])
		}
		f := File{Status: FileStatus(meta[4][0]), Path: fields[1]}
		if f.Status == statusTypeChange {
			f.Status = StatusModified
		}
		fields = fields[2:]
		if f.Status == StatusRenamed && len(fields) > 0 {
			f.OldPath, f.Path, fields = f.Path, fields[0], fields[1:]
		}
		files = append(files, f)
		if len(files) > maxChangedFiles {
			return nil, &TooManyFilesError{Count: len(files) + countRecords(fields)}
		}
		for k := range 2 {
			id, b := diffSide(meta[k], meta[2+k])
			ids = append(ids, id)
			blobs = append(blobs, b)
		}
	}
	if err := r.readLimited(ctx, ids, blobs); err != nil {
		return nil, err
	}
	r.keepCache(ids, blobs)
	for i := range files {
		f := &files[i]
		f.OldText, f.NewText = f.use(blobs[2*i]), f.use(blobs[2*i+1])
		if f.Err == nil && isTooBig(f.OldText, f.NewText) {
			f.Err = ErrTooBig
		}
		if f.Err != nil {
			f.OldText, f.NewText = "", ""
		}
	}
	slices.SortFunc(files, func(a, b File) int { return strings.Compare(a.Path, b.Path) })
	return files, nil
}

// diffSide returns "" if there is no object to read.
func diffSide(mode, id string) (string, blob) {
	switch {
	case strings.Trim(id, "0") == "": // this side has no file
		return "", blob{}
	case mode == gitlink:
		return "", blob{data: "Subproject commit " + id + "\n"}
	}
	return id, blob{}
}

// use sets f.Err from b. ErrBinary takes priority over ErrTooBig.
func (f *File) use(b blob) string {
	if f.Err == nil || errors.Is(b.err, ErrBinary) {
		f.Err = cmp.Or(b.err, f.Err)
	}
	return b.data
}

type blob struct {
	data string
	err  error
	read bool // false if the object is missing or was not read
	size int
}

func (b blob) kept() int {
	if b.size > maxFileSize {
		return 0
	}
	return b.size
}

// countRecords does not look for ':', because a path can start with ':'.
func countRecords(fields []string) int {
	n := 0
	for len(fields) > 0 {
		paths := 1
		if meta := strings.Fields(fields[0]); len(meta) == 5 &&
			(meta[4][0] == byte(StatusRenamed) || meta[4][0] == 'C') {
			paths = 2
		}
		fields = fields[min(1+paths, len(fields)):]
		n++
	}
	return n
}

// fromCache returns the IDs still to read, with "" for cached ones.
func (r *Repo) fromCache(ids []string, blobs []blob) (fresh []string) {
	r.blobMu.Lock()
	defer r.blobMu.Unlock()
	fresh = slices.Clone(ids)
	for i, id := range ids {
		if b, ok := r.blobs[id]; ok && id != "" {
			blobs[i], fresh[i] = b, ""
		}
	}
	return fresh
}

func (r *Repo) keepCache(ids []string, blobs []blob) {
	next := make(map[string]blob, len(ids))
	for i, id := range ids {
		if id != "" && blobs[i].read {
			next[id] = blobs[i]
		}
	}
	r.blobMu.Lock()
	r.blobs = next
	r.blobMu.Unlock()
}

// readLimited keeps the text within r.maxDiffBytes. limitTotal chooses
// the files to leave out, so the same files are left out each time.
func (r *Repo) readLimited(ctx context.Context, ids []string, blobs []blob) error {
	fresh := r.fromCache(ids, blobs)
	var left atomic.Int64
	left.Store(int64(r.maxDiffBytes))
	for _, b := range blobs {
		left.Add(-int64(b.kept()))
	}
	err := errNoRoom // the objects from the cache can already be too many
	if left.Load() >= 0 {
		err = r.readBlobs(ctx, fresh, blobs, &left)
	}
	if !errors.Is(err, errNoRoom) {
		return err
	}
	for i := range fresh {
		if blobs[i].read {
			fresh[i] = "" // read before there was no room
		}
	}
	if err := r.limitTotal(ctx, fresh, blobs); err != nil {
		return err
	}
	return r.readBlobs(ctx, fresh, blobs, nil)
}

// In limitTotal, blobs holds the old and new version of each file in turn.
func (r *Repo) limitTotal(ctx context.Context, fresh []string, blobs []blob) error {
	sizes, err := r.sizes(ctx, fresh)
	if err != nil {
		return err
	}
	total := 0
	for i := 0; i+1 < len(blobs); i += 2 {
		size := 0
		for k := i; k < i+2; k++ {
			size += blobs[k].kept()
			if sizes[k] <= maxFileSize {
				size += sizes[k]
			}
		}
		if total+size <= r.maxDiffBytes {
			total += size
			continue
		}
		for k := i; k < i+2; k++ {
			blobs[k], fresh[k] = blob{err: errDiffTooBig}, ""
		}
	}
	return nil
}

// sizes returns 0 for an empty ID or a missing object.
func (r *Repo) sizes(ctx context.Context, ids []string) ([]int, error) {
	sizes := make([]int, len(ids))
	var in strings.Builder
	var asked []int
	for i, id := range ids {
		if id != "" {
			in.WriteString(id)
			in.WriteString("\n")
			asked = append(asked, i)
		}
	}
	if len(asked) == 0 {
		return sizes, nil
	}
	out, err := r.runInput(ctx, nil, strings.NewReader(in.String()), "cat-file", "--batch-check")
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != len(asked) {
		return nil, fmt.Errorf("git cat-file: %d lines for %d objects", len(lines), len(asked))
	}
	for k, l := range lines {
		if f := strings.Fields(l); len(f) == 3 {
			sizes[asked[k]], _ = strconv.Atoi(f[2])
		}
	}
	return sizes, nil
}

var errNoRoom = errors.New("no room left for the objects")

// readBlobs uses several git processes, because decompressing is slow.
func (r *Repo) readBlobs(ctx context.Context, ids []string, blobs []blob, left *atomic.Int64) error {
	var batch []int
	for i, id := range ids {
		if id != "" {
			batch = append(batch, i)
		}
	}
	n := min(runtime.GOMAXPROCS(0), maxCatFiles, len(batch)/minCatFileBatch)
	if n <= 1 {
		return r.catFile(ctx, ids, batch, blobs, left)
	}
	errs := make([]error, n)
	var wg sync.WaitGroup
	for k := range n {
		part := batch[k*len(batch)/n : (k+1)*len(batch)/n]
		wg.Go(func() { errs[k] = r.catFile(ctx, ids, part, blobs, left) })
	}
	wg.Wait()
	return errors.Join(errs...)
}

const (
	minCatFileBatch = 128
	maxCatFiles     = 8
)

func (r *Repo) catFile(ctx context.Context, ids []string, batch []int, blobs []blob, left *atomic.Int64) error {
	if len(batch) == 0 {
		return nil
	}
	var in strings.Builder
	for _, i := range batch {
		in.WriteString(ids[i])
		in.WriteString("\n")
	}
	cmd, stderr := r.command(ctx, nil, "git", "cat-file", "--batch")
	cmd.Stdin = strings.NewReader(in.String())
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("git cat-file: %w", proc.Error(ctx, "git", err, nil))
	}
	err = readBatch(bufio.NewReader(out), batch, blobs, left)
	if err != nil {
		_ = cmd.Process.Kill()
	}
	if werr := cmd.Wait(); err == nil && werr != nil {
		err = proc.Error(ctx, "git", werr, stderr.Bytes())
	}
	if err != nil {
		return fmt.Errorf("git cat-file: %w", err)
	}
	return nil
}

var errShort = errors.New("short output")

// readBatch reads "<oid> <type> <size>\n<content>\n" records. A missing or
// ambiguous object has only one line.
func readBatch(r *bufio.Reader, batch []int, blobs []blob, left *atomic.Int64) error {
	buf := make([]byte, 32<<10)
	for _, i := range batch {
		hdr, err := r.ReadString('\n')
		if err != nil {
			return errShort
		}
		f := strings.Fields(hdr)
		if len(f) != 3 {
			continue
		}
		n, err := strconv.Atoi(f[2])
		if err != nil {
			return fmt.Errorf("bad size %q: %w", f[2], err)
		}
		if f[1] != "blob" || n > maxFileSize {
			if _, err := r.Discard(n + 1); err != nil {
				return errShort
			}
			if f[1] == "blob" {
				blobs[i] = blob{err: ErrTooBig, read: true, size: n}
			}
			continue
		}
		if left != nil && left.Add(-int64(n)) < 0 {
			return errNoRoom
		}
		var b strings.Builder
		b.Grow(n)
		if k, _ := io.CopyBuffer(&b, io.LimitReader(r, int64(n)), buf); k != int64(n) {
			return errShort
		}
		if _, err := r.Discard(1); err != nil { // the newline after the content
			return errShort
		}
		if s := b.String(); isBinary(s) {
			blobs[i].err = ErrBinary
		} else {
			blobs[i].data = s
		}
		blobs[i].read, blobs[i].size = true, n
	}
	return nil
}

func isBinary(s string) bool { return strings.Contains(s, "\x00") }

// isTooBig counts a final line without a newline, as diff.Lines does.
func isTooBig(versions ...string) bool {
	for _, s := range versions {
		lines := strings.Count(s, "\n")
		if s != "" && !strings.HasSuffix(s, "\n") {
			lines++
		}
		if len(s) > maxFileSize || lines > maxLines {
			return true
		}
	}
	return false
}

// ReadFile reads the regular file at path, relative to the root. It does
// not follow symbolic links out of the root.
func (r *Repo) ReadFile(path string) (string, error) {
	root, err := os.OpenRoot(r.root)
	if err != nil {
		return "", err
	}
	defer func() { _ = root.Close() }()
	info, err := root.Stat(path)
	if err != nil {
		return "", err
	}
	// A device or a named pipe can block the read forever.
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%q is not a regular file", path)
	}
	if info.Size() > maxFileSize {
		return "", ErrTooBig
	}
	b, err := root.ReadFile(path)
	if err != nil {
		return "", err
	}
	switch s := string(b); {
	case isBinary(s):
		return "", ErrBinary
	case isTooBig(s):
		return "", ErrTooBig
	default:
		return s, nil
	}
}

// Contains reports whether dir is the root or below it. It does not run
// git, so it does not see nested repositories.
func (r *Repo) Contains(dir string) bool {
	dir = evalSymlinks(dir)
	return dir == r.root || strings.HasPrefix(dir, r.root+string(filepath.Separator))
}

// Owns reports whether dir is in the working tree of r and not in a
// nested repository. An error means that the answer is unknown.
func (r *Repo) Owns(ctx context.Context, dir string) (bool, error) {
	if !filepath.IsAbs(dir) || !r.Contains(dir) {
		return false, nil
	}
	cmd, stderr := r.command(ctx, nil, "git", "-C", dir, "rev-parse", "--show-toplevel")
	out, err := output(ctx, cmd, stderr)
	// Only a normal exit means that dir is not in a working tree.
	if ee, ok := errors.AsType[*exec.ExitError](err); ok && ee.Exited() {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("git rev-parse: %w", err)
	}
	top := strings.TrimSuffix(out, "\n")
	return top != "" && evalSymlinks(top) == r.root, nil
}

func evalSymlinks(p string) string {
	if q, err := filepath.EvalSymlinks(p); err == nil {
		return q
	}
	return p
}
