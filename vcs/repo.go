// Package vcs reads the changes in a git repository and finds the pull
// request for its current branch.
//
// A [Repo] compares trees, identified by their git tree object IDs.
// [Repo.SnapshotTree] records the working tree as a tree object, and
// [Repo.Diff] returns the files that differ between two trees. [Open]
// takes the environment as a value, as in [exec.Cmd.Env].
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

// emptyTree is the ID of the tree that has no files.
const emptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

// Size limits for one version of a file. They keep the diff and the view
// of a file fast. [Repo.Diff] applies them to the old and new versions
// separately, and [Repo.ReadFile] to the file it reads.
const (
	maxFileSize = 2_000_000 // bytes
	maxLines    = 50_000
)

// Limits for a whole diff. [Repo.Diff] keeps the text of both versions of
// every changed file, so without a limit a large change could fill the
// memory.
const (
	// maxDiffBytes is the most text, old and new versions together, that
	// Diff reads. A caller can hold two diffs while it reloads.
	maxDiffBytes = 128 << 20
	// maxChangedFiles is the most changed files that Diff returns.
	maxChangedFiles = 20_000
)

var (
	// ErrBinary means that a file is binary.
	ErrBinary = errors.New("binary file")
	// ErrTooBig means that a file is larger than 2,000,000 bytes or has
	// more than 50,000 lines.
	ErrTooBig = errors.New("file too large")
	// errDiffTooBig means that the changed files together are larger than
	// maxDiffBytes, so [Repo.Diff] does not read this one. It wraps
	// ErrTooBig.
	errDiffTooBig = fmt.Errorf("changes too large: %w", ErrTooBig)
	// errNoCommits means that HEAD does not point to a commit yet, as in a
	// new repository.
	errNoCommits = errors.New("no commits yet")
)

// Repo is a git repository.
type Repo struct {
	root  string
	index string   // the path of the repository's real index
	env   []string // the environment for git and the forge tools

	// snapLock guards the private index of SnapshotTree and the fields
	// below it. Sending acquires it and receiving releases it, so a waiting
	// caller can give up when its ctx ends.
	snapLock chan struct{}
	snapDir  string // the directory of the private index, or "" before the first snapshot
	snapSeed string // the size and modification time of the real index when it was last copied
	closed   bool

	// blobs caches the objects that the last Diff read, keyed by object
	// ID. blobMu guards blobs.
	blobMu sync.Mutex
	blobs  map[string]blob

	// maxDiffBytes is the limit that Diff applies. Tests lower it.
	maxDiffBytes int
}

// errClosed is the error that SnapshotTree returns after Close.
var errClosed = errors.New("the repository is closed")

// Open returns the repository that contains dir. The repository runs git
// and the forge tools with the environment env, and looks them up in the
// PATH from env. Open does not modify env.
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
	// Ask for each path with a separate git command, because a path can
	// contain a newline. A path can also end with a space, so remove only
	// the final newline.
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

// Close removes the temporary files that [Repo.SnapshotTree] creates. If a
// SnapshotTree call is running, Close waits for it to finish.
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

// run runs git in the root directory and returns its standard output. env
// holds extra variables on top of the repository's environment.
func (r *Repo) run(ctx context.Context, env []string, args ...string) (string, error) {
	return r.runInput(ctx, env, nil, args...)
}

// runInput is like run, but git reads its standard input from in.
func (r *Repo) runInput(ctx context.Context, env []string, in io.Reader, args ...string) (string, error) {
	cmd, stderr := r.command(ctx, env, "git", args...)
	cmd.Stdin = in
	out, err := output(ctx, cmd, stderr)
	if err != nil {
		return out, fmt.Errorf("git %s: %w", args[0], err)
	}
	return out, nil
}

// command returns a command that runs the program name with args in the
// root directory. env holds extra variables on top of the repository's
// environment. The returned buffer collects the standard error.
func (r *Repo) command(ctx context.Context, env []string, name string, args ...string) (*exec.Cmd, *bytes.Buffer) {
	// Set PWD, as exec does when Env is nil. Concat does not modify r.env.
	env = slices.Concat(r.env, []string{"PWD=" + r.root}, env)
	cmd := proc.Command(ctx, env, name, args...)
	cmd.Dir = r.root
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	return cmd, &stderr
}

// output runs cmd and returns its standard output. The error includes the
// first line of standard error, or the cause of ctx if ctx ended. It wraps
// the process error, so callers can check the exit code.
func output(ctx context.Context, cmd *exec.Cmd, stderr *bytes.Buffer) (string, error) {
	out, err := cmd.Output()
	if err != nil {
		return string(out), proc.Error(ctx, cmd.Args[0], err, stderr.Bytes())
	}
	return string(out), nil
}

// isExitOne reports whether err comes from a process that exited with
// status 1.
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

// HeadTree returns the tree ID of HEAD. If HEAD is a branch with no
// commits yet, as in a new repository, it returns the empty tree.
func (r *Repo) HeadTree(ctx context.Context) (string, error) {
	t, err := r.Tree(ctx, "HEAD")
	if err == nil {
		return t, nil
	}
	// rev-parse exits with status 1 if HEAD does not point to a commit yet.
	// A broken HEAD, for example one whose commit is missing, is an error.
	_, rerr := r.run(ctx, nil, "rev-parse", "-q", "--verify", "HEAD")
	if isExitOne(rerr) {
		return emptyTree, nil
	}
	return "", err
}

// ParentTree returns the tree ID of the first parent of the commit rev.
// If rev has no parent, it returns the empty tree.
func (r *Repo) ParentTree(ctx context.Context, rev string) (string, error) {
	c, err := r.ResolveRev(ctx, rev+"^{commit}")
	if err != nil {
		return "", err
	}
	// The output lists the parents, one per line.
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

// MergeBaseTree returns the tree ID of the commit that
// [Repo.MergeBaseCommit] returns.
func (r *Repo) MergeBaseTree(ctx context.Context, base string) (string, error) {
	c, err := r.MergeBaseCommit(ctx, base)
	if err != nil {
		return "", err
	}
	return r.Tree(ctx, c)
}

// MergeBaseCommit returns the best common ancestor commit of base and
// HEAD, as git merge-base finds it. If HEAD has no commits yet, it returns
// an error that says so. If git exits with another error, the error wraps
// the [*exec.ExitError] of git.
func (r *Repo) MergeBaseCommit(ctx context.Context, base string) (string, error) {
	out, err := r.run(ctx, nil, "merge-base", "--end-of-options", base, "HEAD")
	if err != nil {
		// rev-parse exits with status 1 if HEAD does not point to a commit.
		if _, rerr := r.run(ctx, nil, "rev-parse", "-q", "--verify", "HEAD"); isExitOne(rerr) {
			return "", errNoCommits
		}
		return "", fmt.Errorf("no merge base with %s: %w", base, err)
	}
	return strings.TrimSpace(out), nil
}

// SnapshotTree writes the tracked and untracked files in the working tree
// as a tree object and returns its ID. It skips ignored files and files
// that git cannot add, such as a nested repository with no commits. It
// uses a private index, so the real index does not change. [Repo.Close]
// removes the private index. Only one SnapshotTree call runs at a time; if
// ctx ends while a call waits for another, it returns the cause of ctx.
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

// privateIndex returns the environment variable that points git at the
// private index. It copies the real index again whenever the real index
// changes, and creates a new directory if the old one was deleted. The
// caller must hold snapLock.
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

// removeIgnored removes from the private index in env the files that git
// now ignores and that the real index does not track. "git add -A" does
// not remove such files, for example after a .gitignore file changes.
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

// copyIndex copies the real index to dst. If there is no real index, it
// does nothing. The copy keeps the modification time of the original,
// because git uses it to detect files that changed in the same second as
// the index was written.
func (r *Repo) copyIndex(dst string) error {
	info, err := os.Stat(r.index)
	if errors.Is(err, fs.ErrNotExist) {
		return nil // a new repository has no index
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

// FileStatus is the kind of change to a file. Its value is the status
// letter that git uses, for example 'A'.
type FileStatus byte

// The values of FileStatus that [Repo.Diff] returns.
const (
	StatusAdded    FileStatus = 'A'
	StatusModified FileStatus = 'M'
	StatusDeleted  FileStatus = 'D'
	StatusRenamed  FileStatus = 'R'

	// statusTypeChange is a change of file type, for example from a regular
	// file to a symbolic link. [Repo.Diff] reports it as [StatusModified].
	statusTypeChange FileStatus = 'T'
)

// File is a file that differs between two trees. OldText and NewText are
// its content in the old and new tree. They are empty if the file is
// binary, too big, or missing from that tree. For a submodule, each
// version is the single line "Subproject commit <id>".
type File struct {
	Path             string
	OldPath          string // the old path of a renamed file
	Status           FileStatus
	OldText, NewText string
	// Err tells why OldText and NewText are empty: ErrBinary if a version
	// contains a NUL byte, ErrTooBig if a version is too big, and an error
	// that wraps ErrTooBig if the files before it already hold too much
	// text. Err is nil if the file was read. A version larger than
	// 2,000,000 bytes gives ErrTooBig even if it is binary.
	Err error
}

// gitlink is the mode of a submodule in a tree.
const gitlink = "160000"

// TooManyFilesError is the error that [Repo.Diff] returns when more than
// 20,000 files differ.
type TooManyFilesError struct {
	Count int // the number of files that differ
}

// Error returns the number of files and says that they are too many.
func (e *TooManyFilesError) Error() string {
	return fmt.Sprintf("%d files changed, too many to show", e.Count)
}

// Diff returns the files that differ between oldTree and newTree, sorted
// by path. If more than 20,000 files differ, it reads none of them and
// returns a [*TooManyFilesError].
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
	var ids []string // the old and new object ID of each file, or "" if there is none
	var blobs []blob // the content for each entry of ids
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
		for k := range 2 { // the old side, then the new side
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

// diffSide returns the ID of the object to read for one side of a diff,
// given the mode and ID from git diff. If there is no object to read, it
// returns "" and a blob that already holds the content, if any.
func diffSide(mode, id string) (string, blob) {
	switch {
	case strings.Trim(id, "0") == "": // this side has no file
		return "", blob{}
	case mode == gitlink:
		return "", blob{data: "Subproject commit " + id + "\n"}
	}
	return id, blob{}
}

// use returns the content of b. If b is binary or too big, use sets
// f.Err. ErrBinary takes priority over ErrTooBig.
func (f *File) use(b blob) string {
	if f.Err == nil || errors.Is(b.err, ErrBinary) {
		f.Err = cmp.Or(b.err, f.Err)
	}
	return b.data
}

// blob is the content of an object. If the object is binary or too big,
// data is empty and err is ErrBinary or ErrTooBig. read is true if git
// returned the object, and false if the object is missing or was not
// read.
type blob struct {
	data string
	err  error
	read bool
	size int // the size of the object, if read
}

// kept returns the number of bytes that b counts for in the limit of a
// diff: the size of the object, or 0 if it is too big to keep.
func (b blob) kept() int {
	if b.size > maxFileSize {
		return 0
	}
	return b.size
}

// countRecords returns the number of files in the rest of the output of
// git diff --raw -z, split at NUL bytes. Each file is a field of metadata
// followed by its path, or by two paths for a rename or a copy. It reads
// the fields in that order rather than looking for the ':' that starts the
// metadata, because a path can also start with ':'.
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

// fromCache copies into blobs[i] the object ids[i] if the previous Diff
// read it. It returns the IDs still to read, with "" for the others.
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

// keepCache makes the cache hold the objects of ids that were read, and
// no others.
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

// readLimited is like readBlobs, but it reuses the objects that the
// previous call read, and keeps the text within r.maxDiffBytes. blobs
// holds the old and new version of each file in turn. If the objects do
// not fit, limitTotal chooses the files to leave out, so that they are
// the same each time, and readLimited reads the rest.
func (r *Repo) readLimited(ctx context.Context, ids []string, blobs []blob) error {
	fresh := r.fromCache(ids, blobs)
	var left atomic.Int64 // the bytes that the fresh objects can take
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

// limitTotal keeps the text that Diff holds within r.maxDiffBytes. blobs
// holds the old and new version of each file in turn, and fresh the IDs
// that are still to be read. Going through the files in order, limitTotal
// gives both versions of each file that does not fit the error
// errDiffTooBig, and removes them from fresh so that they are not read.
// It asks git for the sizes of the fresh objects.
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
			if sizes[k] <= maxFileSize { // a larger object is not kept
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

// sizes returns the size in bytes of each object ids[i], or 0 if ids[i]
// is "" or the object is missing.
func (r *Repo) sizes(ctx context.Context, ids []string) ([]int, error) {
	sizes := make([]int, len(ids))
	var in strings.Builder
	var asked []int // the indices of the objects asked for
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
	// Each line is "<oid> <type> <size>", or "<oid> missing".
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

// errNoRoom means that the objects did not fit in the bytes left for
// them.
var errNoRoom = errors.New("no room left for the objects")

// readBlobs reads the object ids[i] into blobs[i] for each non-empty
// ids[i]. It does not keep the content of an object larger than
// maxFileSize. If left is not nil, each object takes its size from left,
// and readBlobs fails with errNoRoom once left would fall below zero. It
// splits a large batch across several git processes, because
// decompressing objects is slow.
func (r *Repo) readBlobs(ctx context.Context, ids []string, blobs []blob, left *atomic.Int64) error {
	var batch []int // the indices of the objects to read
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

// Limits for the parallel reads in readBlobs. Each git process reads at
// least minCatFileBatch objects, and at most maxCatFiles processes run at
// once.
const (
	minCatFileBatch = 128
	maxCatFiles     = 8
)

// catFile reads the object ids[i] into blobs[i] for each i in batch,
// using one git process. left is as in readBlobs.
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

// errShort means that the output of git cat-file ended too early.
var errShort = errors.New("short output")

// readBatch reads the output of "git cat-file --batch" into blobs[i] for
// each i in batch. For each object, the output is either
// "<oid> <type> <size>\n<content>\n" or a single line that ends with
// "missing" or "ambiguous". left is as in readBlobs.
func readBatch(r *bufio.Reader, batch []int, blobs []blob, left *atomic.Int64) error {
	buf := make([]byte, 32<<10) // the copy buffer, reused for every object
	for _, i := range batch {
		hdr, err := r.ReadString('\n')
		if err != nil {
			return errShort
		}
		f := strings.Fields(hdr)
		if len(f) != 3 { // "<id> missing" or "<id> ambiguous"
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
		// Read the content straight into the string, without an extra copy.
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

// isBinary reports whether s contains a NUL byte.
func isBinary(s string) bool { return strings.Contains(s, "\x00") }

// isTooBig reports whether any of the versions is larger than maxFileSize
// bytes or has more than maxLines lines. A final line without a newline
// counts as a line, as in diff.Lines.
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

// ReadFile reads the file at path in the working tree. The path is relative
// to the root. ReadFile reads only regular files inside the root, including
// through symbolic links that stay inside it. For anything else, such as a
// directory or a named pipe, it returns an error. If the file is binary or
// too big, ReadFile returns ErrBinary or ErrTooBig.
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
	// Do not read a device or a named pipe, because the read can block or
	// never end.
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

// Contains reports whether dir is the root or a directory below it. It
// follows symbolic links, so on macOS /tmp and /private/tmp are the same.
// Contains does not run git, so it does not detect nested repositories.
func (r *Repo) Contains(dir string) bool {
	dir = evalSymlinks(dir)
	return dir == r.root || strings.HasPrefix(dir, r.root+string(filepath.Separator))
}

// Owns reports whether dir is in the working tree of r and not in another
// repository or worktree below the root. It reports false for a relative
// dir. Owns runs git only if [Repo.Contains] reports true. An error means
// that git could not run, so the answer is unknown.
func (r *Repo) Owns(ctx context.Context, dir string) (bool, error) {
	if !filepath.IsAbs(dir) || !r.Contains(dir) {
		return false, nil
	}
	cmd, stderr := r.command(ctx, nil, "git", "-C", dir, "rev-parse", "--show-toplevel")
	out, err := output(ctx, cmd, stderr)
	// If a signal killed git, for example because ctx ended, the answer is
	// unknown. Only a normal exit means that dir is not in a working tree.
	if ee, ok := errors.AsType[*exec.ExitError](err); ok && ee.Exited() {
		return false, nil // dir is not in any working tree
	}
	if err != nil {
		return false, fmt.Errorf("git rev-parse: %w", err)
	}
	top := strings.TrimSuffix(out, "\n")
	return top != "" && evalSymlinks(top) == r.root, nil
}

// evalSymlinks returns p with its symbolic links resolved, or p unchanged
// if that fails.
func evalSymlinks(p string) string {
	if q, err := filepath.EvalSymlinks(p); err == nil {
		return q
	}
	return p
}
