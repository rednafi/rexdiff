package vcs_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"
	"unicode/utf8"

	"github.com/rednafi/rexdiff/internal/proc"
	"github.com/rednafi/rexdiff/internal/testgit"
	"github.com/rednafi/rexdiff/vcs"
)

// testRepo is the fixture repository that openTestRepo copies. It has two
// commits: "init" on main and "b" on the branch feat, which is checked
// out. The working tree has an uncommitted edit, an untracked file, and
// ignored files.
var testRepo = testgit.NewFixture(func(tb testing.TB, dir string) {
	tb.Helper()
	testgit.InitRepo(tb, dir)
	run := func(args ...string) {
		tb.Helper()
		testgit.Git(tb, dir, args...)
	}
	testgit.WriteFile(tb, dir, "a.txt", "one\n")
	run("add", ".")
	run("commit", "-qm", "init")
	run("checkout", "-qb", "feat")
	testgit.WriteFile(tb, dir, "b.txt", "bee\n")
	run("add", ".")
	run("commit", "-qm", "b")
	testgit.WriteFile(tb, dir, "a.txt", "one\ntwo\n")
	testgit.WriteFile(tb, dir, "new.txt", "untracked\n")
	testgit.WriteFile(tb, dir, ".gitignore", "ignored.txt\nbuild/\n")
	testgit.WriteFile(tb, dir, "ignored.txt", "x\n")
	testgit.WriteFile(tb, dir, "build/out", "x\n")
})

// openTestRepo copies testRepo to a new directory and returns the opened
// repository and its directory. The repository uses the environment from
// [testgit.ToolEnv].
func openTestRepo(t *testing.T) (*vcs.Repo, string) {
	t.Helper()
	return openTestRepoEnv(t, testgit.ToolEnv(t))
}

// openTestRepoEnv is like openTestRepo, but the repository uses the
// environment env.
func openTestRepoEnv(t *testing.T, env []string) (*vcs.Repo, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "a repo ") // the trailing space must survive
	testRepo.Copy(t, dir)

	// Open a subdirectory, from which Open must find the root.
	return openEnv(t, filepath.Join(dir, "build"), env), dir
}

// open opens the repository at dir with the environment from
// [testgit.ToolEnv], and closes it when the test ends.
func open(t *testing.T, dir string) *vcs.Repo {
	t.Helper()
	return openEnv(t, dir, testgit.ToolEnv(t))
}

// openEnv is like open, but the repository uses the environment env.
func openEnv(t *testing.T, dir string, env []string) *vcs.Repo {
	t.Helper()
	r, err := vcs.Open(t.Context(), env, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

// gitLink returns an environment whose PATH holds only a new directory,
// and the path of a symbolic link to git in that directory. A test removes
// the link to make git unavailable to a repository that is already open.
func gitLink(t *testing.T) (env []string, link string) {
	t.Helper()
	bin := t.TempDir()
	testgit.LinkGit(t, bin)
	return append(testgit.Env(), "PATH="+bin), filepath.Join(bin, "git")
}

// removeGit removes the link to git that gitLink created.
func removeGit(t *testing.T, link string) {
	t.Helper()
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
}

// snapshot returns the ID of a tree that records the working tree.
func snapshot(t *testing.T, r *vcs.Repo) string {
	t.Helper()
	tree, err := r.SnapshotTree(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

// head returns the ID of the tree of HEAD.
func head(t *testing.T, r *vcs.Repo) string {
	t.Helper()
	tree, err := r.HeadTree(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

// parent returns the tree ID of the first parent of rev.
func parent(t *testing.T, r *vcs.Repo, rev string) string {
	t.Helper()
	tree, err := r.ParentTree(t.Context(), rev)
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

// changes returns the files that differ between oldTree and newTree.
func changes(t *testing.T, r *vcs.Repo, oldTree, newTree string) []vcs.File {
	t.Helper()
	fs, err := r.Diff(t.Context(), oldTree, newTree)
	if err != nil {
		t.Fatal(err)
	}
	return fs
}

// names returns the status letter and path of each file, separated by
// spaces, for example "Aa.txt Mb.txt".
func names(fs []vcs.File) string {
	var n []string
	for _, f := range fs {
		n = append(n, string(f.Status)+f.Path)
	}
	return strings.Join(n, " ")
}

// uncommitted returns, in the format of names, the files that differ
// between HEAD and the working tree.
func uncommitted(t *testing.T, r *vcs.Repo) string {
	t.Helper()
	return names(changes(t, r, head(t, r), snapshot(t, r)))
}

// paths returns the path of each entry, with "!" in front of ignored
// paths.
func paths(es []vcs.Entry) []string {
	var out []string
	for _, e := range es {
		if e.Ignored {
			out = append(out, "!"+e.Path)
		} else {
			out = append(out, e.Path)
		}
	}
	return out
}

// TestScopes checks the changes that each review scope shows: uncommitted
// changes, the branch since its merge base, the last agent turn, and a
// single commit. It also checks that a snapshot does not change the real
// index.
func TestScopes(t *testing.T) {
	t.Parallel()
	r, dir := openTestRepo(t)
	ctx := t.Context()

	now := snapshot(t, r)
	unc := changes(t, r, head(t, r), now)
	if got := names(unc); got != "A.gitignore Ma.txt Anew.txt" {
		t.Fatalf("Diff(HEAD, snapshot) = %s, want A.gitignore Ma.txt Anew.txt", got)
	}
	if unc[1].OldText != "one\n" || unc[1].NewText != "one\ntwo\n" {
		t.Errorf("Diff(HEAD, snapshot) for a.txt: Old %q, New %q, want %q, %q",
			unc[1].OldText, unc[1].NewText, "one\n", "one\ntwo\n")
	}

	mb, err := r.MergeBaseTree(ctx, "main")
	if err != nil {
		t.Fatal(err)
	}
	if got := names(changes(t, r, mb, now)); got != "A.gitignore Ma.txt Ab.txt Anew.txt" {
		t.Errorf("Diff(merge base, snapshot) = %s, want A.gitignore Ma.txt Ab.txt Anew.txt", got)
	}
	_, err = r.MergeBaseTree(ctx, "nosuchbranch")
	if _, ok := errors.AsType[*exec.ExitError](err); !ok {
		t.Errorf("MergeBaseTree(nosuchbranch) = %v, want the error of git", err)
	}

	// At the start of a turn, rexdiff takes a snapshot, and then the agent
	// edits a file.
	testgit.WriteFile(t, dir, "new.txt", "untracked\nmore\n")
	tf := changes(t, r, now, snapshot(t, r))
	if got := names(tf); got != "Mnew.txt" || tf[0].NewText != "untracked\nmore\n" {
		t.Errorf("Diff(turn start, snapshot) = %s, want Mnew.txt with the new line", got)
	}

	// SnapshotTree must not change the real index.
	st := testgit.Git(t, dir, "status", "--porcelain")
	if !strings.Contains(st, "?? new.txt") {
		t.Errorf("git status after Snapshot = %q, want ?? new.txt", st)
	}

	cs, err := r.Commits(ctx, "")
	if err != nil || len(cs) != 2 || cs[0].Subject != "b" || cs[1].Subject != "init" {
		t.Fatalf("Commits = %+v, %v, want b and init", cs, err)
	}
	tree, err := r.Tree(ctx, cs[0].SHA)
	if err != nil {
		t.Fatal(err)
	}
	if got := names(changes(t, r, parent(t, r, cs[0].SHA), tree)); got != "Ab.txt" {
		t.Errorf("Diff(parent of b, b) = %s, want Ab.txt", got)
	}
	// emptyTree is the ID of the tree that has no files.
	const emptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"
	if got := parent(t, r, cs[1].SHA); got != emptyTree {
		t.Errorf("parent of the root commit = %s, want the empty tree", got)
	}
}

// TestMissingCommit checks that HeadTree and ParentTree return an error,
// not the empty tree, when a commit object is missing.
func TestMissingCommit(t *testing.T) {
	t.Parallel()
	// Returning the empty tree would be wrong, because then every file
	// would appear as added.
	r, dir := openTestRepo(t)
	ctx := t.Context()
	cs, err := r.Commits(ctx, "")
	if err != nil || len(cs) != 2 {
		t.Fatalf("Commits = %+v, %v, want two commits", cs, err)
	}
	remove := func(sha string) {
		t.Helper()
		obj := filepath.Join(dir, ".git", "objects", sha[:2], sha[2:])
		if err := os.Remove(obj); err != nil {
			t.Fatal(err)
		}
	}
	remove(cs[1].SHA)
	if id, err := r.ParentTree(ctx, "HEAD"); err == nil {
		t.Errorf("ParentTree(HEAD) = %s with no parent commit, want an error", id)
	}
	if id, err := r.ParentTree(ctx, "nope"); err == nil {
		t.Errorf("ParentTree(nope) = %s, want an error", id)
	}
	remove(cs[0].SHA)
	if id, err := r.HeadTree(ctx); err == nil {
		t.Errorf("HeadTree = %s with no HEAD commit, want an error", id)
	}
}

// TestDiffManyFiles checks that Diff returns the right content for enough
// files to need several git processes, and that a second Diff returns the
// new content of a file edited in between.
func TestDiffManyFiles(t *testing.T) {
	t.Parallel()
	r, dir := openTestRepo(t)
	const n = 600
	name := func(i int) string { return fmt.Sprintf("many/f%03d.txt", i) }
	for i := range n {
		testgit.WriteFile(t, dir, name(i), fmt.Sprintf("old %d\n", i))
	}
	testgit.Git(t, dir, "add", ".")
	testgit.Git(t, dir, "commit", "-qm", "many")
	for i := range n {
		testgit.WriteFile(t, dir, name(i), fmt.Sprintf("new %d\n", i))
	}
	check := func(edited string) {
		t.Helper()
		got := map[string]vcs.File{}
		for _, f := range changes(t, r, head(t, r), snapshot(t, r)) {
			got[f.Path] = f
		}
		for i := range n {
			want := fmt.Sprintf("new %d\n", i)
			if i == 7 && edited != "" {
				want = edited
			}
			if f := got[name(i)]; f.OldText != fmt.Sprintf("old %d\n", i) || f.NewText != want {
				t.Fatalf("%s: Old %q, New %q, want %q", name(i), f.OldText, f.NewText, want)
			}
		}
	}
	check("")
	testgit.WriteFile(t, dir, name(7), "edited\n")
	check("edited\n")
}

// TestDiffSkipsBinaryAndBigFiles checks that Diff and ReadFile report
// binary and oversized files with ErrBinary and ErrTooBig and drop their
// content, and that the limits apply to each version separately.
func TestDiffSkipsBinaryAndBigFiles(t *testing.T) {
	t.Parallel()
	r, dir := openTestRepo(t)
	full := strings.Repeat("x", 1_999_999) + "\n" // exactly at the byte limit
	lines := strings.Repeat("x\n", 50_000)        // exactly at the line limit
	// A file whose old and new versions are both at the limit is not too
	// big.
	limit := strings.Repeat("y\n", 50_000)
	testgit.WriteFile(t, dir, "bytes", full)
	testgit.WriteFile(t, dir, "lines", lines)
	testgit.WriteFile(t, dir, "limit", limit)
	testgit.Git(t, dir, "add", ".")
	testgit.Git(t, dir, "commit", "-qm", "base")
	testgit.WriteFile(t, dir, "bytes", "y"+full)
	testgit.WriteFile(t, dir, "lines", "y\n"+lines)
	testgit.WriteFile(t, dir, "limit", "z"+limit[2:])
	err := os.CopyFS(dir, fstest.MapFS{
		"bin": {Data: []byte("a\x00b")},
		// The NUL byte in "late" is at offset 9000.
		"late": {Data: []byte(strings.Repeat("x", 9000) + "\x00")},
		"big":  {Data: []byte(strings.Repeat("x\n", 50_001))},
		"huge": {Data: []byte(strings.Repeat("x", 2_000_001))},
		// Diff reads this file after the skipped ones, so skipping must not
		// break the reads that follow.
		"small": {Data: []byte("after\n")},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]vcs.File{}
	for _, f := range changes(t, r, head(t, r), snapshot(t, r)) {
		got[f.Path] = f
	}
	for _, p := range []string{"bin", "late"} {
		if f := got[p]; !errors.Is(f.Err, vcs.ErrBinary) || f.NewText != "" {
			t.Errorf("%s: %+v, want Err ErrBinary with no New", p, f)
		}
	}
	for _, p := range []string{"bytes", "lines", "big", "huge"} {
		if f := got[p]; !errors.Is(f.Err, vcs.ErrTooBig) || f.OldText != "" || f.NewText != "" {
			t.Errorf("%s: Err = %v, Old has %d bytes, New has %d bytes, want ErrTooBig, 0, 0",
				p, f.Err, len(f.OldText), len(f.NewText))
		}
	}
	if f := got["limit"]; f.Err != nil || f.NewText != "z"+limit[2:] {
		t.Errorf("limit: Err = %v, New has %d bytes, want nil, %d",
			f.Err, len(f.NewText), len(limit)-1)
	}
	if f := got["small"]; f.NewText != "after\n" {
		t.Errorf("small: New = %q, want %q", f.NewText, "after\n")
	}
	for p, want := range map[string]error{
		"bin":   vcs.ErrBinary,
		"late":  vcs.ErrBinary,
		"big":   vcs.ErrTooBig,
		"huge":  vcs.ErrTooBig,
		"bytes": vcs.ErrTooBig,
		"lines": vcs.ErrTooBig,
		"limit": nil,
	} {
		if _, err := r.ReadFile(p); !errors.Is(err, want) {
			t.Errorf("ReadFile(%s): err = %v, want %v", p, err, want)
		}
	}
}

// TestDiffTotalLimit checks that Diff stops reading files once their
// text passes the limit, keeps the files after them that still fit, and
// gives the same result when the objects come from its cache.
func TestDiffTotalLimit(t *testing.T) {
	t.Parallel()
	r, dir := openTestRepo(t)
	big := strings.Repeat("x", 999) + "\n"
	for _, p := range []string{"big1", "big2", "big3"} {
		testgit.WriteFile(t, dir, p, big)
	}
	// The other changes take less than 100 bytes, so big3 is the file that
	// passes the limit, and new.txt after it still fits.
	r.SetMaxDiffBytes(2100)
	for range 2 {
		got := map[string]vcs.File{}
		for _, f := range changes(t, r, head(t, r), snapshot(t, r)) {
			got[f.Path] = f
		}
		for _, p := range []string{"big1", "big2"} {
			if f := got[p]; f.Err != nil || f.NewText != big {
				t.Errorf("%s: Err = %v, New has %d bytes, want nil, %d", p, f.Err, len(f.NewText), len(big))
			}
		}
		if f := got["big3"]; !errors.Is(f.Err, vcs.ErrTooBig) || f.NewText != "" {
			t.Errorf("big3: Err = %v, New has %d bytes, want ErrTooBig, 0", f.Err, len(f.NewText))
		}
		if f := got["new.txt"]; f.Err != nil || f.NewText != "untracked\n" {
			t.Errorf("new.txt: Err = %v, New = %q, want nil, %q", f.Err, f.NewText, "untracked\n")
		}
	}
}

// TestDiffTooManyFiles checks that Diff reads no file and reports the
// number of changed files when there are more than 20,000. The count is
// right when paths start with ':', as git's metadata does, and when files
// are renamed, which gives two paths. git fast-import writes the two
// commits, so the test writes no files.
func TestDiffTooManyFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	testgit.InitRepo(t, dir)
	const renamed, added = 5, 20_000
	var b strings.Builder
	b.WriteString("blob\nmark :1\ndata 2\nx\n")
	for i := range renamed {
		text := fmt.Sprintf("file %d\nwith some text\n", i)
		fmt.Fprintf(&b, "blob\nmark :%d\ndata %d\n%s", i+2, len(text), text)
	}
	b.WriteString("commit refs/heads/main\ncommitter a <a@b> 0 +0000\ndata 4\nold\n")
	for i := range renamed {
		fmt.Fprintf(&b, "M 100644 :%d old/%d\n", i+2, i)
	}
	b.WriteString("commit refs/heads/main\ncommitter a <a@b> 1 +0000\ndata 4\nnew\n")
	for i := range renamed {
		fmt.Fprintf(&b, "R old/%d :renamed%d\n", i, i)
	}
	for i := range added {
		fmt.Fprintf(&b, "M 100644 :1 :added%d\n", i)
	}
	cmd := proc.Command(t.Context(), testgit.ToolEnv(t), "git", "fast-import", "--quiet")
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(b.String())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git fast-import: %v: %s", err, out)
	}
	r := open(t, dir)
	oldTree, newTree := parent(t, r, "main"), strings.TrimSpace(testgit.Git(t, dir, "rev-parse", "main^{tree}"))
	_, err := r.Diff(t.Context(), oldTree, newTree)
	if e, ok := errors.AsType[*vcs.TooManyFilesError](err); !ok || e.Count != renamed+added {
		t.Errorf("Diff: err = %v, want a TooManyFilesError with Count = %d", err, renamed+added)
	}
}

// TestFilesInConflict checks that Files lists a file with a merge conflict
// only once.
func TestFilesInConflict(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	testgit.InitRepo(t, dir)
	run := func(args ...string) {
		t.Helper()
		testgit.Git(t, dir, args...)
	}
	testgit.WriteFile(t, dir, "f.txt", "base\n")
	run("add", ".")
	run("commit", "-qm", "base")
	run("checkout", "-qb", "other")
	testgit.WriteFile(t, dir, "f.txt", "other\n")
	run("commit", "-qam", "other")
	run("checkout", "-q", "main")
	testgit.WriteFile(t, dir, "f.txt", "main\n")
	run("commit", "-qam", "main")
	cmd := proc.Command(t.Context(), testgit.ToolEnv(t), "git", "merge", "other")
	cmd.Dir = dir
	if err := cmd.Run(); err == nil {
		t.Fatal("git merge other: no error, want a conflict")
	}
	r := open(t, dir)
	es, err := r.Files(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := paths(es), []string{"f.txt"}; !slices.Equal(got, want) {
		t.Errorf("Files = %q, want %q", got, want)
	}
}

// TestWorktree checks Files, IgnoredDir, Grep, ReadFile and Contains on
// the working tree of the test repository.
func TestWorktree(t *testing.T) {
	t.Parallel()
	r, dir := openTestRepo(t)
	ctx := t.Context()

	es, err := r.Files(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := ".gitignore a.txt b.txt !build/ !ignored.txt new.txt"
	if got := strings.Join(paths(es), " "); got != want {
		t.Errorf("Files = %s, want %s", got, want)
	}

	testgit.WriteFile(t, dir, "build/sub/x", "x\n")
	ignored := paths(r.IgnoredDir("build"))
	if want := []string{"!build/out", "!build/sub/"}; !slices.Equal(ignored, want) {
		t.Errorf("IgnoredDir(build) = %q, want %q", ignored, want)
	}
	if es := r.IgnoredDir("no such dir"); len(es) != 0 {
		t.Errorf("IgnoredDir(no such dir) = %v, want none", es)
	}

	// A query with an upper case letter is case-sensitive. A query in
	// lower case ignores case.
	if ms, _, err := r.Grep(ctx, "TWO", 10, time.Minute); err != nil || len(ms) != 0 {
		t.Errorf("Grep(TWO) = %+v, %v, want none, nil", ms, err)
	}
	// git quotes this name unless it is run with -z.
	testgit.WriteFile(t, dir, "a:é\n.txt", "x: Two\n")
	ms, _, err := r.Grep(ctx, "x: two", 10, time.Minute)
	match := vcs.Match{Path: "a:é\n.txt", Line: 1, Text: "x: Two"}
	if err != nil || len(ms) != 1 || ms[0] != match {
		t.Errorf("Grep(x: two) = %+v, %v, want [%+v], nil", ms, err, match)
	}
	if ms, _, err := r.Grep(ctx, "nothing has this", 10, time.Minute); err != nil || len(ms) != 0 {
		t.Errorf("Grep with no match = %+v, %v, want none, nil", ms, err)
	}

	if s, err := r.ReadFile("a.txt"); err != nil || s != "one\ntwo\n" {
		t.Errorf("ReadFile(a.txt) = %q, %v, want %q, nil",
			s, err, "one\ntwo\n")
	}
	tests := []struct {
		path string
		want bool
	}{
		{filepath.Join(dir, "build"), true},
		{filepath.Dir(dir), false},
		{dir + "x", false}, // a sibling directory whose name starts with the root's name
	}
	for _, tt := range tests {
		if got := r.Contains(tt.path); got != tt.want {
			t.Errorf("Contains(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

// TestOwns checks that Owns reports true for the root and a subdirectory,
// and false for a nested repository, a relative path, an empty path, a
// path outside the root, and a missing directory.
func TestOwns(t *testing.T) {
	t.Parallel()
	r, dir := openTestRepo(t)
	nested := filepath.Join(dir, "build", "nested")
	testgit.InitRepo(t, nested)
	tests := []struct {
		dir  string
		want bool
	}{
		{dir, true},
		{filepath.Join(dir, "build"), true},
		{nested, false},
		{"build", false}, // a relative path
		{"", false},
		{filepath.Dir(dir), false},
		{filepath.Join(dir, "gone"), false},
	}
	for _, tt := range tests {
		if got, err := r.Owns(t.Context(), tt.dir); got != tt.want || err != nil {
			t.Errorf("Owns(%q) = %v, %v, want %v, nil", tt.dir, got, err, tt.want)
		}
	}
}

// TestOwnsWithoutGit checks that Owns returns an error when git is
// missing, except for a path outside the root, for which it does not need
// to run git.
func TestOwnsWithoutGit(t *testing.T) {
	t.Parallel()
	env, link := gitLink(t)
	r, dir := openTestRepoEnv(t, env)
	removeGit(t, link)
	if _, err := r.Owns(t.Context(), dir); err == nil {
		t.Error("Owns without git gives no error")
	}
	if got, err := r.Owns(t.Context(), filepath.Dir(dir)); got || err != nil {
		t.Errorf("Owns(outside) without git = %v, %v, want false, nil", got, err)
	}
}

// TestRefs checks Branches, DefaultBase, ResolveRev and Branch, including
// remote branches, a dangling origin/HEAD and a repository with no
// commits.
func TestRefs(t *testing.T) {
	t.Parallel()
	r, dir := openTestRepo(t)
	ctx := t.Context()
	if bs, err := r.Branches(ctx); err != nil || strings.Join(bs, " ") != "feat main" {
		t.Errorf("Branches = %v, %v, want [feat main], nil", bs, err)
	}
	if b := r.DefaultBase(ctx); b != "main" {
		t.Errorf("DefaultBase = %q, want main", b)
	}
	// origin/main hides main, and origin/HEAD is not listed. Branches of
	// remotes other than origin are not listed either. The branch old has
	// the oldest commit, so it comes last.
	for _, remote := range []string{"origin", "up"} {
		testgit.Git(t, dir, "update-ref", "refs/remotes/"+remote+"/main", "main")
		testgit.Git(t, dir, "symbolic-ref", "refs/remotes/"+remote+"/HEAD",
			"refs/remotes/"+remote+"/main")
	}
	obj := t.TempDir()
	const who = "A <a@example.com> 946684800 +0000\n"
	testgit.WriteFile(t, obj, "old", "tree "+testgit.Git(t, dir, "rev-parse", "main^{tree}")+
		"author "+who+"committer "+who+"\nold\n")
	old := testgit.Git(t, dir, "hash-object", "-t", "commit", "-w", filepath.Join(obj, "old"))
	testgit.Git(t, dir, "update-ref", "refs/heads/old", strings.TrimSpace(old))
	want := "feat origin/main old"
	if bs, err := r.Branches(ctx); err != nil || strings.Join(bs, " ") != want {
		t.Errorf("Branches with remotes = %v, %v, want %s", bs, err, want)
	}
	if _, err := r.ResolveRev(ctx, "nosuchref"); err == nil {
		t.Error("ResolveRev(nosuchref): no error, want an error")
	}
	testgit.Git(t, dir, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/gone")
	if b := r.DefaultBase(ctx); b != "main" {
		t.Errorf("DefaultBase with a dangling origin/HEAD = %q, want main", b)
	}

	empty := t.TempDir()
	testgit.InitRepo(t, empty)
	if b, err := open(t, empty).Branch(ctx); err != nil || b != "main" {
		t.Errorf("Branch with no commit = %q, %v, want main, nil", b, err)
	}
}

// TestEditor checks that GitEditor returns core.editor, or "" if it is not
// set.
func TestEditor(t *testing.T) {
	t.Parallel()
	r, dir := openTestRepo(t)
	if got := r.GitEditor(t.Context()); got != "" {
		t.Errorf("Editor with no core.editor = %q, want \"\"", got)
	}
	testgit.Git(t, dir, "config", "core.editor", "vim -f")
	if got := r.GitEditor(t.Context()); got != "vim -f" {
		t.Errorf("Editor = %q, want \"vim -f\"", got)
	}
}

// TestDiffRenameAndSubmodule checks that Diff sets OldPath for a renamed
// file and returns a "Subproject commit" line for each side of a changed
// submodule.
func TestDiffRenameAndSubmodule(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	testgit.InitRepo(t, dir)
	testgit.WriteFile(t, dir, "old.txt", "one\ntwo\nthree\n")
	const (
		a = "1111111111111111111111111111111111111111"
		b = "2222222222222222222222222222222222222222"
	)
	testgit.Git(t, dir, "add", ".")
	testgit.Git(t, dir, "update-index", "--add", "--cacheinfo", "160000,"+a+",sub")
	testgit.Git(t, dir, "commit", "-qm", "one")
	testgit.Git(t, dir, "mv", "old.txt", "new.txt")
	testgit.Git(t, dir, "update-index", "--cacheinfo", "160000,"+b+",sub")
	testgit.Git(t, dir, "commit", "-qm", "two")

	ctx := t.Context()
	r := open(t, dir)
	oldTree, newTree := parent(t, r, "HEAD"), head(t, r)
	got, err := r.Diff(ctx, oldTree, newTree)
	if err != nil {
		t.Fatal(err)
	}
	want := []vcs.File{
		{
			Path:    "new.txt",
			OldPath: "old.txt",
			Status:  vcs.StatusRenamed,
			OldText: "one\ntwo\nthree\n",
			NewText: "one\ntwo\nthree\n",
		},
		{
			Path:    "sub",
			Status:  vcs.StatusModified,
			OldText: "Subproject commit " + a + "\n",
			NewText: "Subproject commit " + b + "\n",
		},
	}
	if !slices.Equal(got, want) {
		t.Errorf("Diff = \n%+v\nwant:\n%+v", got, want)
	}
}

// TestOpenOutsideARepo checks that Open fails outside a repository.
func TestOpenOutsideARepo(t *testing.T) {
	t.Parallel()
	if _, err := vcs.Open(t.Context(), testgit.ToolEnv(t), t.TempDir()); err == nil {
		t.Error("Open outside a repository: no error, want an error")
	}
}

// TestNewRepo checks Open, Root, HeadTree and SnapshotTree in a repository
// with no commits and no index, including one whose root path contains a
// newline.
func TestNewRepo(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, root string }{
		{"plain root", "repo"},
		{"newline in root", "a\nb"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := filepath.Join(t.TempDir(), tt.root)
			testgit.InitRepo(t, dir)
			testgit.WriteFile(t, dir, "a.txt", "one\n")
			r, err := vcs.Open(t.Context(), testgit.ToolEnv(t), dir)
			if err != nil {
				t.Fatal(err)
			}
			if want, _ := filepath.EvalSymlinks(dir); r.Root() != want {
				t.Errorf("Root = %q, want %q", r.Root(), want)
			}
			if got := uncommitted(t, r); got != "Aa.txt" {
				t.Errorf("Diff(HEAD, Snapshot) = %s, want Aa.txt", got)
			}
		})
	}
}

// TestSnapshotBadIndex checks that SnapshotTree fails if the real index
// exists but cannot be read. Only a missing index is treated as an empty
// one.
func TestSnapshotBadIndex(t *testing.T) {
	t.Parallel()
	r, dir := openTestRepo(t)
	idx := filepath.Join(dir, ".git", "index")
	if err := os.Remove(idx); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("index", idx); err != nil { // a link to itself
		t.Fatal(err)
	}
	if tree, err := r.SnapshotTree(t.Context()); err == nil {
		t.Errorf("Snapshot with a bad index = %s, want an error", tree)
	}
}

// TestSnapshotRacyEdit checks that SnapshotTree notices an edit that keeps
// the file size and is made in the same second as the last write of the
// index.
func TestSnapshotRacyEdit(t *testing.T) {
	t.Parallel()
	// git detects "racily clean" files by comparing with the index's
	// modification time, so the copy of the index must keep that time.
	r, dir := openTestRepo(t)
	testgit.Git(t, dir, "config", "core.trustctime", "false")
	testgit.Git(t, dir, "add", "-A")
	testgit.Git(t, dir, "commit", "-qm", "all")
	then := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	touch := func(name string) {
		t.Helper()
		if err := os.Chtimes(filepath.Join(dir, name), then, then); err != nil {
			t.Fatal(err)
		}
	}
	testgit.WriteFile(t, dir, "b.txt", "bee\n")
	touch("b.txt")
	// Record the modification time of b.txt in the index entry.
	testgit.Git(t, dir, "update-index", "--really-refresh")
	touch(".git/index")
	testgit.WriteFile(t, dir, "b.txt", "BEE\n") // same size; touch then restores the time
	touch("b.txt")

	if got := uncommitted(t, r); got != "Mb.txt" {
		t.Errorf("Diff(HEAD, Snapshot) = %s, want Mb.txt", got)
	}
}

// TestCommitsFirstParent checks that Commits returns only first-parent
// commits after since, the last [vcs.LastCommits] commits when since is
// empty, and no commits in an empty repository.
func TestCommitsFirstParent(t *testing.T) {
	t.Parallel()
	r, dir := openTestRepo(t)
	ctx := t.Context()
	run := func(args ...string) {
		t.Helper()
		testgit.Git(t, dir, args...)
	}
	run("checkout", "-qb", "side", "main")
	run("commit", "-q", "--allow-empty", "-m", "side")
	run("checkout", "-q", "feat")
	run("merge", "-q", "--no-ff", "-m", "merge side", "side")
	for i := range 60 {
		run("commit", "-q", "--allow-empty", "-m", fmt.Sprint("c", i))
	}
	subjects := func(cs []vcs.Commit) []string {
		var s []string
		for _, c := range cs {
			s = append(s, c.Subject)
		}
		return s
	}

	since, err := r.MergeBaseCommit(ctx, "main")
	if err != nil {
		t.Fatal(err)
	}
	cs, err := r.Commits(ctx, since)
	if err != nil {
		t.Fatal(err)
	}
	got := subjects(cs)
	if len(got) != 62 || got[0] != "c59" || got[60] != "merge side" || got[61] != "b" {
		t.Errorf("Commits(fork point) = %d commits %q…, want c59 to b, no side", len(got), got[:min(3, len(got))])
	}

	cs, err = r.Commits(ctx, "")
	if err != nil || len(cs) != vcs.LastCommits || cs[0].Subject != "c59" {
		t.Errorf("Commits(\"\") = %d commits, %v, want the last %d", len(cs), err, vcs.LastCommits)
	}

	empty := t.TempDir()
	testgit.InitRepo(t, empty)
	er := open(t, empty)
	if cs, err := er.Commits(ctx, ""); err != nil || len(cs) != 0 {
		t.Errorf("Commits with no commit = %v, %v, want none, no error", cs, err)
	}
}

// TestGrepLimit checks that Grep returns at most limit matches, in file
// TestGrepLongLines checks that Grep keeps only the start of a long line,
// without cutting a character in two, and reads the matches after it.
func TestGrepLongLines(t *testing.T) {
	t.Parallel()
	r, dir := openTestRepo(t)
	long := "hit " + strings.Repeat("é", 1_000_000)
	testgit.WriteFile(t, dir, "long.txt", long+"\n"+long+"\nhit short\n")
	ms, more, err := r.Grep(t.Context(), "hit", 10, time.Minute)
	if err != nil || more || len(ms) != 3 {
		t.Fatalf("Grep = %d matches, more %t, %v, want 3, false, nil", len(ms), more, err)
	}
	for _, m := range ms[:2] {
		if len(m.Text) > 512 || len(m.Text) < 500 || !utf8.ValidString(m.Text) || !strings.HasPrefix(long, m.Text) {
			t.Errorf("line %d: Text has %d bytes, want the start of the line, valid UTF-8, at most 512 bytes", m.Line, len(m.Text))
		}
	}
	if want := (vcs.Match{Path: "long.txt", Line: 3, Text: "hit short"}); ms[2] != want {
		t.Errorf("third match = %+v, want %+v", ms[2], want)
	}
}

// order, and sets more only when there are more matches.
func TestGrepLimit(t *testing.T) {
	t.Parallel()
	r, dir := openTestRepo(t)
	ctx := t.Context()
	testgit.WriteFile(t, dir, "many.txt", strings.Repeat("hit\n", 5000))
	tests := []struct {
		limit    int
		wantN    int
		wantMore bool
	}{
		{10, 10, true},
		{4999, 4999, true},
		{5000, 5000, false},
		{6000, 5000, false},
	}
	for _, tt := range tests {
		ms, more, err := r.Grep(ctx, "hit", tt.limit, time.Minute)
		if err != nil || len(ms) != tt.wantN || more != tt.wantMore {
			t.Errorf("Grep(hit, %d) = %d matches, more %t, %v, want %d, %t, nil",
				tt.limit, len(ms), more, err, tt.wantN, tt.wantMore)
		}
	}
	want := vcs.Match{Path: "many.txt", Line: 2, Text: "hit"}
	if ms, _, _ := r.Grep(ctx, "hit", 2, time.Minute); len(ms) != 2 || ms[1] != want {
		t.Errorf("Grep(hit, 2) = %+v, want many.txt:2 second", ms)
	}
}

// TestGrepCancelled checks that Grep returns the cancellation cause of a
// cancelled context.
func TestGrepCancelled(t *testing.T) {
	t.Parallel()
	r, _ := openTestRepo(t)
	ctx, cancel := context.WithCancelCause(t.Context())
	errStop := errors.New("the test stops the search")
	cancel(errStop)
	if _, _, err := r.Grep(ctx, "one", 10, time.Minute); !errors.Is(err, errStop) {
		t.Errorf("Grep with a cancelled context = %v, want the cause %v", err, errStop)
	}
}

// TestSnapshotFollowsIndex checks that after a .gitignore change, a new
// SnapshotTree leaves out a file that git now ignores but keeps a file
// added with "git add -f".
func TestSnapshotFollowsIndex(t *testing.T) {
	t.Parallel()
	r, dir := openTestRepo(t)
	testgit.WriteFile(t, dir, "build.log", "x\n")
	testgit.WriteFile(t, dir, "keep.env", "k\n")
	if got := uncommitted(t, r); got != "A.gitignore Ma.txt Abuild.log Akeep.env Anew.txt" {
		t.Fatalf("Diff(HEAD, Snapshot) = %s, want A.gitignore Ma.txt Abuild.log Akeep.env Anew.txt", got)
	}
	testgit.WriteFile(t, dir, ".gitignore", "ignored.txt\nbuild/\n*.log\n*.env\n")
	testgit.Git(t, dir, "add", ".gitignore")
	testgit.Git(t, dir, "add", "-f", "keep.env")
	testgit.Git(t, dir, "commit", "-qm", "ignore")
	if got := uncommitted(t, r); got != "Ma.txt Anew.txt" {
		t.Errorf("Diff(HEAD, Snapshot) after the ignore = %s, want Ma.txt Anew.txt", got)
	}
}
