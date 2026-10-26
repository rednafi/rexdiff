// Package vcs reads the changes in a git repository and gets the pull
// request of its branch.
//
// A [Repo] compares trees. A tree is the ID of a git tree object.
// [Repo.Snapshot] gets a tree of the working tree. [Repo.Diff] gets the files
// that differ between two trees.
//
// [Open] takes the environment of git and of the forge tools as a value,
// as in [exec.Cmd.Env]. The package does not read the environment of the
// process.
package vcs

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/rednafi/rexdiff/internal/proc"
)

// emptyTree is the ID of the tree that has no files.
const emptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

// The size limits are the same as in herdr-reviewr. [Repo.Diff] applies
// them to the sum of the two versions of a file. [Repo.ReadFile] applies
// them to one file.
const (
	maxFileSize = 2_000_000 // bytes
	maxLines    = 50_000
)

var (
	// ErrBinary means that a file is binary.
	ErrBinary = errors.New("binary file")
	// ErrTooBig means that a file is larger than 2,000,000 bytes or has
	// more than 50,000 lines.
	ErrTooBig = errors.New("file too large")
)

// Repo is a git repository.
type Repo struct {
	root  string
	index string   // the path of the real index
	env   []string // the environment of git and of the forge tools

	// snapLock is the lock of the private index of Snapshot. A send takes
	// the lock, and a receive gives it back. A channel lets Snapshot stop
	// its wait when ctx ends. The lock guards snapDir, snapSeed and
	// closed. snapDir is the directory of the private index, or "" before
	// the first Snapshot. snapSeed is the file status of the real index at
	// the last copy.
	snapLock chan struct{}
	snapDir  string
	snapSeed string
	closed   bool
}

// errClosed is the error of a Snapshot after Close.
var errClosed = errors.New("the repository is closed")

// Open returns the repository that contains dir. The repository runs git
// and the forge tools with the environment env, and finds them in the
// PATH of env. Open does not change env.
func Open(ctx context.Context, dir string, env []string) (*Repo, error) {
	// git gets the root in PWD. PWD must be an absolute path.
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	r := &Repo{root: abs, env: env, snapLock: make(chan struct{}, 1)}
	// Get each path with its own git process, because a path can have a
	// newline. A path can also end with a space, so remove only the last
	// newline.
	root, err := r.run(ctx, nil, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, err
	}
	// Resolve the links once, so Contains and Owns compare real paths.
	r.root = evalSymlinks(strings.TrimSuffix(root, "\n"))
	index, err := r.run(ctx, nil,
		"rev-parse", "--path-format=absolute", "--git-path", "index")
	if err != nil {
		return nil, err
	}
	r.index = strings.TrimSuffix(index, "\n")
	return r, nil
}

// Close removes the files that the repository keeps for [Repo.Snapshot].
// Close waits for the Snapshot that runs.
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
// adds to the environment of the repository.
func (r *Repo) run(ctx context.Context, env []string, args ...string) (string, error) {
	return r.runInput(ctx, env, nil, args...)
}

// runInput is like run. git reads its standard input from in.
func (r *Repo) runInput(ctx context.Context, env []string, in io.Reader, args ...string) (string, error) {
	cmd, stderr := r.command(ctx, env, "git", args...)
	cmd.Stdin = in
	out, err := output(ctx, cmd, stderr)
	if err != nil {
		return out, fmt.Errorf("git %s: %w", args[0], err)
	}
	return out, nil
}

// command returns the command "name args..." in the root directory. env
// adds to the environment of the repository. The buffer gets the standard
// error.
func (r *Repo) command(ctx context.Context, env []string, name string, args ...string) (*exec.Cmd, *bytes.Buffer) {
	// PWD is the directory of the command, as exec sets it for a nil Env.
	// A copy keeps r.env as it is.
	env = slices.Concat(r.env, []string{"PWD=" + r.root}, env)
	cmd := proc.Command(ctx, env, name, args...)
	cmd.Dir = r.root
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	return cmd, &stderr
}

// output runs cmd and returns its standard output. ctx is the context of
// cmd. The error has the first line of the standard error, or the cause of
// ctx. It wraps the error of the process, so callers can examine its exit
// code.
func output(ctx context.Context, cmd *exec.Cmd, stderr *bytes.Buffer) (string, error) {
	out, err := cmd.Output()
	if err != nil {
		return string(out), proc.Error(ctx, cmd.Args[0], err, stderr.Bytes())
	}
	return string(out), nil
}

// exitedOne reports whether err is from a process that exited with code 1.
func exitedOne(err error) bool {
	ee, ok := errors.AsType[*exec.ExitError](err)
	return ok && ee.ExitCode() == 1
}

// Resolve returns the object ID of rev, for example "HEAD" or "main^{tree}".
func (r *Repo) Resolve(ctx context.Context, rev string) (string, error) {
	out, err := r.run(ctx, nil, "rev-parse", "--verify", "--quiet", rev)
	if exitedOne(err) {
		return "", fmt.Errorf("git: unknown revision %q", rev)
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// Tree returns the tree of the commit rev.
func (r *Repo) Tree(ctx context.Context, rev string) (string, error) {
	return r.Resolve(ctx, rev+"^{tree}")
}

// HeadTree returns the tree of HEAD. If HEAD is a branch with no commits yet,
// as in a new repository, it returns the empty tree.
func (r *Repo) HeadTree(ctx context.Context) (string, error) {
	t, err := r.Tree(ctx, "HEAD")
	if err == nil {
		return t, nil
	}
	// rev-parse exits with 1 if HEAD names no commit yet. A broken HEAD,
	// for example one whose commit is missing, is an error.
	_, rerr := r.run(ctx, nil, "rev-parse", "-q", "--verify", "HEAD")
	if exitedOne(rerr) {
		return emptyTree, nil
	}
	return "", err
}

// ParentTree returns the tree of the first parent of the commit rev. If rev has
// no parent, it returns the empty tree.
func (r *Repo) ParentTree(ctx context.Context, rev string) (string, error) {
	c, err := r.Resolve(ctx, rev+"^{commit}")
	if err != nil {
		return "", err
	}
	// The output has the parents, one on each line.
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

// MergeBaseTree returns the tree of the commit that [Repo.MergeBaseCommit]
// returns.
func (r *Repo) MergeBaseTree(ctx context.Context, base string) (string, error) {
	c, err := r.MergeBaseCommit(ctx, base)
	if err != nil {
		return "", err
	}
	return r.Tree(ctx, c)
}

// MergeBaseCommit returns the best common ancestor commit of base and
// HEAD, as git merge-base finds it. If git exits with an error, the error
// wraps the [*exec.ExitError] of git.
func (r *Repo) MergeBaseCommit(ctx context.Context, base string) (string, error) {
	out, err := r.run(ctx, nil, "merge-base", "--end-of-options", base, "HEAD")
	if err != nil {
		return "", fmt.Errorf("no merge base with %s: %w", base, err)
	}
	return strings.TrimSpace(out), nil
}

// Snapshot writes the working tree as a tree object and returns its ID.
// The tree has the tracked and the untracked files. It does not have the
// ignored files. It also does not have the files that git cannot add, for
// example a nested repository with no commit. Snapshot uses a private
// index, so the real index does not change. The private index keeps the
// file status of the last Snapshot, also of the untracked files, so git
// reads only the files that change. [Repo.Close] removes it. One Snapshot
// runs at a time. If ctx ends while Snapshot waits for another,
// Snapshot returns the cause of ctx.
func (r *Repo) Snapshot(ctx context.Context) (string, error) {
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
	if err := r.dropIgnored(ctx, env); err != nil {
		return "", fmt.Errorf("snapshot: %w", err)
	}
	_, err = r.run(ctx, env, "add", "-A", "--ignore-errors")
	if exitedOne(err) {
		err = nil // git cannot add some files, but it adds the others
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

// privateIndex returns the environment that points git at the private
// index of Snapshot. It copies the real index when the real index changes,
// for example after a commit or a "git add -f". A clean of the temporary
// directory makes a new directory. The caller must hold snapLock.
func (r *Repo) privateIndex() ([]string, error) {
	seed := ""
	if st, err := os.Stat(r.index); err == nil {
		seed = fmt.Sprint(st.Size(), st.ModTime().UnixNano())
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

// dropIgnored removes from the private index of env the files that git
// ignores now and that the real index does not have. "add -A" keeps such
// files, for example after an edit of a .gitignore file.
func (r *Repo) dropIgnored(ctx context.Context, env []string) error {
	ignored := []string{"ls-files", "-z", "-c", "-i", "--exclude-standard"}
	out, err := r.run(ctx, env, ignored...)
	if err != nil || out == "" {
		return err
	}
	tracked, err := r.run(ctx, nil, ignored...) // force-added files stay
	if err != nil {
		return err
	}
	keep := map[string]bool{}
	for p := range strings.SplitSeq(tracked, "\x00") {
		keep[p] = true
	}
	var drop strings.Builder
	for p := range strings.SplitSeq(strings.TrimSuffix(out, "\x00"), "\x00") {
		if !keep[p] {
			drop.WriteString(p)
			drop.WriteString("\x00")
		}
	}
	if drop.Len() == 0 {
		return nil
	}
	_, err = r.runInput(ctx, env, strings.NewReader(drop.String()),
		"rm", "--cached", "-q", "--ignore-unmatch", "--pathspec-from-file=-", "--pathspec-file-nul")
	return err
}

// copyIndex copies the real index to dst. If there is no real index, it
// does nothing. The copy keeps the modification time of the index. git
// uses this time to find files that changed in the same second as the
// index.
func (r *Repo) copyIndex(dst string) error {
	st, err := os.Stat(r.index)
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
	return os.Chtimes(dst, time.Time{}, st.ModTime())
}

// FileStatus is the type of change to a file. Its value is the letter that git
// uses, for example 'A'.
type FileStatus byte

// The values of Status that [Repo.Diff] returns.
const (
	StatusAdded    FileStatus = 'A'
	StatusModified FileStatus = 'M'
	StatusDeleted  FileStatus = 'D'
	StatusRenamed  FileStatus = 'R'

	// statusTypeChange is a change of type, for example from a file to a
	// symbolic link. [Repo.Diff] shows it as [StatusModified].
	statusTypeChange FileStatus = 'T'
)

// File is a file that is different between two trees. Old and New are the
// two versions of its content. They are empty if the file is binary, too
// big, or not in the tree. For a submodule, each version is one line,
// "Subproject commit <id>", as git diff shows it.
type File struct {
	Path     string
	From     string // for a rename, the old path
	Status   FileStatus
	Old, New string
	// Binary is true if a version of the file has a NUL byte.
	Binary bool
	// TooBig is true if the two versions together are larger than
	// 2,000,000 bytes or have more than 50,000 lines.
	TooBig bool
}

// gitlink is the mode of a submodule in a tree.
const gitlink = "160000"

// Diff returns the files that are different between the trees from and to,
// sorted by path.
func (r *Repo) Diff(ctx context.Context, from, to string) ([]File, error) {
	out, err := r.run(ctx, nil,
		"diff", "--raw", "-z", "--no-abbrev", "--find-renames",
		"--end-of-options", from, to)
	if err != nil {
		return nil, err
	}
	// Each file is ":<old mode> <new mode> <old id> <new id> <status>",
	// then its path. A rename has its old and new paths.
	fields := strings.Split(strings.TrimSuffix(out, "\x00"), "\x00")
	var files []File
	var ids []string // the old and the new object of each file, or "" for none
	var blobs []blob // the content of each ID
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
			f.From, f.Path, fields = f.Path, fields[0], fields[1:]
		}
		files = append(files, f)
		for k := range 2 { // the old side, then the new side
			id, b := diffSide(meta[k], meta[2+k])
			ids = append(ids, id)
			blobs = append(blobs, b)
		}
	}
	if err := r.readBlobs(ctx, ids, blobs); err != nil {
		return nil, err
	}
	for i := range files {
		f := &files[i]
		f.Old, f.New = f.use(blobs[2*i]), f.use(blobs[2*i+1])
		f.TooBig = f.TooBig || isTooBig(f.Old, f.New)
		if f.Binary || f.TooBig {
			f.Old, f.New = "", ""
		}
	}
	slices.SortFunc(files, func(a, b File) int { return strings.Compare(a.Path, b.Path) })
	return files, nil
}

// diffSide returns the object to read for one side of a diff, from its
// mode and its ID. It returns "" if there is no object to read. Then the
// blob has the content, if any.
func diffSide(mode, id string) (string, blob) {
	switch {
	case strings.Trim(id, "0") == "": // this side has no file
		return "", blob{}
	case mode == gitlink:
		return "", blob{data: "Subproject commit " + id + "\n"}
	}
	return id, blob{}
}

// use returns the content of b, and marks f if b is binary or too big.
func (f *File) use(b blob) string {
	f.Binary = f.Binary || errors.Is(b.err, ErrBinary)
	f.TooBig = f.TooBig || errors.Is(b.err, ErrTooBig)
	return b.data
}

// blob is the content of an object. If the object is binary or too big,
// data is empty and err is ErrBinary or ErrTooBig.
type blob struct {
	data string
	err  error
}

// readBlobs reads the objects with the given IDs into blobs[i] for each
// ids[i], with one git process. It skips an empty ID. It does not keep the
// content of an object that is larger than maxFileSize.
func (r *Repo) readBlobs(ctx context.Context, ids []string, blobs []blob) error {
	var batch []int // the indices of the objects to read
	var in strings.Builder
	for i, id := range ids {
		if id != "" {
			batch = append(batch, i)
			in.WriteString(id)
			in.WriteString("\n")
		}
	}
	if len(batch) == 0 {
		return nil
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
	err = readBatch(bufio.NewReader(out), batch, blobs)
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

// errShort is the error of output that ends too soon.
var errShort = errors.New("short output")

// readBatch reads the output of "git cat-file --batch" into blobs[i] for
// each i in batch. For each object, the output has
// "<oid> <type> <size>\n<content>\n", or one line that ends with "missing"
// or "ambiguous".
func readBatch(r *bufio.Reader, batch []int, blobs []blob) error {
	for _, i := range batch {
		hdr, err := r.ReadString('\n')
		if err != nil {
			return errShort
		}
		f := strings.Fields(hdr)
		if len(f) != 3 { // "<id> missing"
			continue
		}
		n, err := strconv.Atoi(f[2])
		if err != nil {
			return fmt.Errorf("bad size %q", f[2])
		}
		if f[1] != "blob" || n > maxFileSize {
			if _, err := r.Discard(n + 1); err != nil {
				return errShort
			}
			if f[1] == "blob" {
				blobs[i].err = ErrTooBig
			}
			continue
		}
		b := make([]byte, n+1)
		if _, err := io.ReadFull(r, b); err != nil {
			return errShort
		}
		if s := string(b[:n]); isBinary(s) {
			blobs[i].err = ErrBinary
		} else {
			blobs[i].data = s
		}
	}
	return nil
}

// isBinary reports whether s has a NUL byte.
func isBinary(s string) bool { return strings.Contains(s, "\x00") }

// isTooBig reports whether the versions together are larger than
// maxFileSize bytes or have more than maxLines lines. A last line without
// a newline counts as a line, as in diff.Lines.
func isTooBig(versions ...string) bool {
	size, lines := 0, 0
	for _, s := range versions {
		size += len(s)
		lines += strings.Count(s, "\n")
		if s != "" && !strings.HasSuffix(s, "\n") {
			lines++
		}
	}
	return size > maxFileSize || lines > maxLines
}

// ReadFile reads the file at path in the working tree. The path is relative
// to the root. ReadFile reads only a regular file in the root, also through
// a symbolic link. For another file, for example a directory or a named
// pipe, it returns an error. If the file is binary or too big, ReadFile
// returns ErrBinary or ErrTooBig.
func (r *Repo) ReadFile(path string) (string, error) {
	root, err := os.OpenRoot(r.root)
	if err != nil {
		return "", err
	}
	defer func() { _ = root.Close() }()
	st, err := root.Stat(path)
	if err != nil {
		return "", err
	}
	// Do not read a device or a named pipe. The read can block or not end.
	if !st.Mode().IsRegular() {
		return "", fmt.Errorf("%q is not a regular file", path)
	}
	if st.Size() > maxFileSize {
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
// follows symbolic links, so /tmp and /private/tmp on macOS are the same.
// Contains does not run git, thus it does not see a nested repository.
func (r *Repo) Contains(dir string) bool {
	dir = evalSymlinks(dir)
	return dir == r.root || strings.HasPrefix(dir, r.root+string(filepath.Separator))
}

// Owns reports whether dir is in the working tree of r, and not in another
// repository or worktree below the root. As in herdr-reviewr, a relative
// dir is not in it. Owns runs git only if [Repo.Contains] reports true.
// An error means that git cannot run, and the answer is not known.
func (r *Repo) Owns(ctx context.Context, dir string) (bool, error) {
	if !filepath.IsAbs(dir) || !r.Contains(dir) {
		return false, nil
	}
	cmd, stderr := r.command(ctx, nil, "git", "-C", dir, "rev-parse", "--show-toplevel")
	out, err := output(ctx, cmd, stderr)
	if _, ok := errors.AsType[*exec.ExitError](err); ok {
		return false, nil // dir is in no working tree
	}
	if err != nil {
		return false, fmt.Errorf("git rev-parse: %w", err)
	}
	top := strings.TrimSuffix(out, "\n")
	return top != "" && evalSymlinks(top) == r.root, nil
}

// evalSymlinks returns p with its symbolic links resolved, or p if that
// fails.
func evalSymlinks(p string) string {
	if q, err := filepath.EvalSymlinks(p); err == nil {
		return q
	}
	return p
}
