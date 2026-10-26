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

	"github.com/rednafi/rexdiff/internal/proc"
	"github.com/rednafi/rexdiff/internal/testgit"
	"github.com/rednafi/rexdiff/vcs"
)

// testRepo is the repository of newRepo. It has two commits: "init" on
// main and "b" on the branch feat. The working tree has uncommitted
// changes, an untracked file, and an ignored file.
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

// newRepo copies testRepo to a new directory, and returns the repository
// and its directory. The repository has the environment of
// [testgit.ToolEnv].
func newRepo(t *testing.T) (*vcs.Repo, string) {
	t.Helper()
	return newRepoEnv(t, testgit.ToolEnv(t))
}

// newRepoEnv is like newRepo, but the repository has the environment env.
func newRepoEnv(t *testing.T, env []string) (*vcs.Repo, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "a repo ") // git keeps the space
	testRepo.Copy(t, dir)

	// Open from a subdirectory. Open must find the root.
	return openEnv(t, filepath.Join(dir, "build"), env), dir
}

// open opens the repository at dir with the environment of
// [testgit.ToolEnv], and closes it at the end of the test.
func open(t *testing.T, dir string) *vcs.Repo {
	t.Helper()
	return openEnv(t, dir, testgit.ToolEnv(t))
}

// openEnv is like open, but the repository has the environment env.
func openEnv(t *testing.T, dir string, env []string) *vcs.Repo {
	t.Helper()
	r, err := vcs.Open(t.Context(), env, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

// gitLink returns an environment with a PATH of only a new directory, and
// the link to git in that directory. A test removes the link to take git
// away from a repository that is open.
func gitLink(t *testing.T) (env []string, link string) {
	t.Helper()
	bin := t.TempDir()
	testgit.LinkGit(t, bin)
	return append(testgit.Env(), "PATH="+bin), filepath.Join(bin, "git")
}

// removeGit removes the link to git of gitLink.
func removeGit(t *testing.T, link string) {
	t.Helper()
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
}

// snapshot returns the tree of the working tree.
func snapshot(t *testing.T, r *vcs.Repo) string {
	t.Helper()
	tree, err := r.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

// head returns the tree of HEAD.
func head(t *testing.T, r *vcs.Repo) string {
	t.Helper()
	tree, err := r.HeadTree(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

// parent returns the tree of the parent of rev.
func parent(t *testing.T, r *vcs.Repo, rev string) string {
	t.Helper()
	tree, err := r.ParentTree(t.Context(), rev)
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

// changes returns the files that differ from the tree from to the tree to.
func changes(t *testing.T, r *vcs.Repo, from, to string) []vcs.File {
	t.Helper()
	fs, err := r.Diff(t.Context(), from, to)
	if err != nil {
		t.Fatal(err)
	}
	return fs
}

// names returns the status and the path of each file, for example
// "Aa.txt Mb.txt".
func names(fs []vcs.File) string {
	var n []string
	for _, f := range fs {
		n = append(n, string(f.Status)+f.Path)
	}
	return strings.Join(n, " ")
}

// uncommitted returns the names of the files that differ from HEAD to the
// working tree.
func uncommitted(t *testing.T, r *vcs.Repo) string {
	t.Helper()
	return names(changes(t, r, head(t, r), snapshot(t, r)))
}

// paths returns the path of each entry. An ignored path starts with "!".
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

// TestScopes checks the trees that each scope of a review compares.
func TestScopes(t *testing.T) {
	t.Parallel()
	r, dir := newRepo(t)
	ctx := t.Context()

	now := snapshot(t, r)
	unc := changes(t, r, head(t, r), now)
	if got := names(unc); got != "A.gitignore Ma.txt Anew.txt" {
		t.Fatalf("uncommitted: %s, want A.gitignore Ma.txt Anew.txt", got)
	}
	if unc[1].Old != "one\n" || unc[1].New != "one\ntwo\n" {
		t.Errorf("a.txt: Old %q, New %q, want %q, %q",
			unc[1].Old, unc[1].New, "one\n", "one\ntwo\n")
	}

	mb, err := r.MergeBaseTree(ctx, "main")
	if err != nil {
		t.Fatal(err)
	}
	if got := names(changes(t, r, mb, now)); got != "A.gitignore Ma.txt Ab.txt Anew.txt" {
		t.Errorf("branch: %s, want A.gitignore Ma.txt Ab.txt Anew.txt", got)
	}
	_, err = r.MergeBaseTree(ctx, "nosuchbranch")
	if _, ok := errors.AsType[*exec.ExitError](err); !ok {
		t.Errorf("MergeBaseTree(nosuchbranch) = %v, want the error of git", err)
	}

	// In a turn, rexdiff takes a snapshot and then the agent edits a file.
	testgit.WriteFile(t, dir, "new.txt", "untracked\nmore\n")
	tf := changes(t, r, now, snapshot(t, r))
	if got := names(tf); got != "Mnew.txt" || tf[0].New != "untracked\nmore\n" {
		t.Errorf("turn: %s, want Mnew.txt with the new line", got)
	}

	// Snapshot must not change the real index.
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
		t.Errorf("commit b: %s, want Ab.txt", got)
	}
	// emptyTree is the ID of the tree that has no files.
	const emptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"
	if got := parent(t, r, cs[1].SHA); got != emptyTree {
		t.Errorf("parent of the root commit = %s, want the empty tree", got)
	}
}

// TestMissingCommit checks that HeadTree and ParentTree fail when a commit
// object is missing.
func TestMissingCommit(t *testing.T) {
	t.Parallel()
	// The empty tree is not a correct result. It shows every file as added.
	r, dir := newRepo(t)
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

// TestDiffSkipsBinaryAndBigFiles checks that Diff and ReadFile mark binary
// and big files, and do not read their content. The limits are the same as
// in herdr-reviewr. Diff adds the sizes of the two versions.
func TestDiffSkipsBinaryAndBigFiles(t *testing.T) {
	t.Parallel()
	r, dir := newRepo(t)
	half := strings.Repeat("x", 1_100_000) + "\n" // two make 2,200,002 bytes
	lines := strings.Repeat("x\n", 30_000)        // two make 60,000 lines
	// Two versions with 25,000 lines each are not too big.
	limit := strings.Repeat("y\n", 25_000)
	testgit.WriteFile(t, dir, "bytes", half)
	testgit.WriteFile(t, dir, "lines", lines)
	testgit.WriteFile(t, dir, "limit", limit)
	testgit.Git(t, dir, "add", ".")
	testgit.Git(t, dir, "commit", "-qm", "base")
	testgit.WriteFile(t, dir, "bytes", "y"+half)
	testgit.WriteFile(t, dir, "lines", "y"+lines)
	testgit.WriteFile(t, dir, "limit", "z"+limit)
	err := os.CopyFS(dir, fstest.MapFS{
		"bin": {Data: []byte("a\x00b")},
		// The NUL of "late" is at byte 9000.
		"late": {Data: []byte(strings.Repeat("x", 9000) + "\x00")},
		"big":  {Data: []byte(strings.Repeat("x\n", 50_001))},
		"huge": {Data: []byte(strings.Repeat("x", 2_000_001))},
		// Diff reads this file after the skipped content.
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
		if f := got[p]; !errors.Is(f.Skip, vcs.ErrBinary) || f.New != "" {
			t.Errorf("%s: %+v, want Skip ErrBinary with no New", p, f)
		}
	}
	for _, p := range []string{"bytes", "lines", "big", "huge"} {
		if f := got[p]; !errors.Is(f.Skip, vcs.ErrTooBig) || f.Old != "" || f.New != "" {
			t.Errorf("%s: Skip = %v, Old has %d bytes, New has %d bytes, want ErrTooBig, 0, 0",
				p, f.Skip, len(f.Old), len(f.New))
		}
	}
	if f := got["limit"]; f.Skip != nil || f.New != "z"+limit {
		t.Errorf("limit: Skip = %v, New has %d bytes, want nil, %d",
			f.Skip, len(f.New), len(limit)+1)
	}
	if f := got["small"]; f.New != "after\n" {
		t.Errorf("small: New = %q, want %q", f.New, "after\n")
	}
	for p, want := range map[string]error{
		"bin":   vcs.ErrBinary,
		"late":  vcs.ErrBinary,
		"big":   vcs.ErrTooBig,
		"huge":  vcs.ErrTooBig,
		"bytes": nil,
	} {
		if _, err := r.ReadFile(p); !errors.Is(err, want) {
			t.Errorf("ReadFile(%s): err = %v, want %v", p, err, want)
		}
	}
}

// TestFilesInConflict checks that Files lists a path in a merge conflict
// once.
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

// TestWorktree checks Files, Grep, ReadFile, and Contains in the working
// tree.
func TestWorktree(t *testing.T) {
	t.Parallel()
	r, dir := newRepo(t)
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

	ms, _, err := r.Grep(ctx, "TWO", 10, time.Minute)
	match := vcs.Match{Path: "a.txt", Line: 2, Text: "two"}
	if err != nil || len(ms) != 1 || ms[0] != match {
		t.Errorf("Grep(TWO) = %+v, %v, want [%+v], nil", ms, err, match)
	}
	// git quotes this name unless -z.
	testgit.WriteFile(t, dir, "a:é\n.txt", "x: two\n")
	ms, _, err = r.Grep(ctx, "x: two", 10, time.Minute)
	match = vcs.Match{Path: "a:é\n.txt", Line: 1, Text: "x: two"}
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
		{dir + "x", false}, // a sibling with the same prefix
	}
	for _, tt := range tests {
		if got := r.Contains(tt.path); got != tt.want {
			t.Errorf("Contains(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

// TestOwns checks which directories are in the working tree, as in
// herdr-reviewr. A subdirectory is in it. A nested repository, a relative
// path, and an outside path are not.
func TestOwns(t *testing.T) {
	t.Parallel()
	r, dir := newRepo(t)
	nested := filepath.Join(dir, "build", "nested")
	testgit.InitRepo(t, nested)
	tests := []struct {
		dir  string
		want bool
	}{
		{dir, true},
		{filepath.Join(dir, "build"), true},
		{nested, false},
		{"build", false}, // relative
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

// TestOwnsWithoutGit checks that Owns fails without git. No git runs for
// a path outside the root.
func TestOwnsWithoutGit(t *testing.T) {
	t.Parallel()
	env, link := gitLink(t)
	r, dir := newRepoEnv(t, env)
	removeGit(t, link)
	if _, err := r.Owns(t.Context(), dir); err == nil {
		t.Error("Owns without git gives no error")
	}
	if got, err := r.Owns(t.Context(), filepath.Dir(dir)); got || err != nil {
		t.Errorf("Owns(outside) without git = %v, %v, want false, nil", got, err)
	}
}

// TestRefs checks Branches, DefaultBase, and Resolve.
func TestRefs(t *testing.T) {
	t.Parallel()
	r, dir := newRepo(t)
	ctx := t.Context()
	if bs, err := r.Branches(ctx); err != nil || strings.Join(bs, " ") != "feat main" {
		t.Errorf("Branches = %v, %v, want [feat main], nil", bs, err)
	}
	if b := r.DefaultBase(ctx); b != "main" {
		t.Errorf("DefaultBase = %q, want main", b)
	}
	// The HEAD of each remote is not a branch.
	for _, remote := range []string{"origin", "up"} {
		testgit.Git(t, dir, "update-ref", "refs/remotes/"+remote+"/main", "main")
		testgit.Git(t, dir, "symbolic-ref", "refs/remotes/"+remote+"/HEAD",
			"refs/remotes/"+remote+"/main")
	}
	want := "feat main origin/main up/main"
	if bs, err := r.Branches(ctx); err != nil || strings.Join(bs, " ") != want {
		t.Errorf("Branches with remotes = %v, %v, want %s", bs, err, want)
	}
	if _, err := r.Resolve(ctx, "nosuchref"); err == nil {
		t.Error("Resolve(nosuchref): no error, want an error")
	}
}

// TestEditor checks that Editor returns core.editor, or "" if it is not set.
func TestEditor(t *testing.T) {
	t.Parallel()
	r, dir := newRepo(t)
	if got := r.Editor(t.Context()); got != "" {
		t.Errorf("Editor with no core.editor = %q, want \"\"", got)
	}
	testgit.Git(t, dir, "config", "core.editor", "vim -f")
	if got := r.Editor(t.Context()); got != "vim -f" {
		t.Errorf("Editor = %q, want \"vim -f\"", got)
	}
}

// TestDiffRenameAndSubmodule checks the old path of a rename and the
// content that Diff gives for a submodule.
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
	from, to := parent(t, r, "HEAD"), head(t, r)
	got, err := r.Diff(ctx, from, to)
	if err != nil {
		t.Fatal(err)
	}
	want := []vcs.File{
		{
			Path:   "new.txt",
			From:   "old.txt",
			Status: vcs.StatusRenamed,
			Old:    "one\ntwo\nthree\n",
			New:    "one\ntwo\nthree\n",
		},
		{
			Path:   "sub",
			Status: vcs.StatusModified,
			Old:    "Subproject commit " + a + "\n",
			New:    "Subproject commit " + b + "\n",
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

// TestNewRepo checks Open, Root, HeadTree, and Snapshot in a repository
// with no commit and no index. The path of a root can have a newline.
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

// TestSnapshotBadIndex checks that Snapshot fails if it cannot read the
// real index. Only a missing index means no index.
func TestSnapshotBadIndex(t *testing.T) {
	t.Parallel()
	r, dir := newRepo(t)
	idx := filepath.Join(dir, ".git", "index")
	if err := os.Remove(idx); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("index", idx); err != nil { // a loop
		t.Fatal(err)
	}
	if tree, err := r.Snapshot(t.Context()); err == nil {
		t.Errorf("Snapshot with a bad index = %s, want an error", tree)
	}
}

// TestSnapshotRacyEdit checks that Snapshot sees a same-size edit in the
// same second as the last write of the index.
func TestSnapshotRacyEdit(t *testing.T) {
	t.Parallel()
	// git checks such "racily clean" files by their content. It finds them by
	// the modification time of the index. The copy of the index must keep
	// that time.
	r, dir := newRepo(t)
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
	// The index entry gets the time of b.txt.
	testgit.Git(t, dir, "update-index", "--really-refresh")
	touch(".git/index")
	testgit.WriteFile(t, dir, "b.txt", "BEE\n") // the same size and time
	touch("b.txt")

	if got := uncommitted(t, r); got != "Mb.txt" {
		t.Errorf("Diff(HEAD, Snapshot) = %s, want Mb.txt", got)
	}
}

// TestCommitsLikeHerdrReviewr checks the commits of the commit picker.
// They are the first-parent commits after since, else the last 50.
func TestCommitsLikeHerdrReviewr(t *testing.T) {
	t.Parallel()
	r, dir := newRepo(t)
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

// TestGrepLimit checks that Grep stops at the limit and tells that there
// are more matches.
func TestGrepLimit(t *testing.T) {
	t.Parallel()
	r, dir := newRepo(t)
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

// TestGrepCancelled checks that Grep returns the cause of a cancelled
// context.
func TestGrepCancelled(t *testing.T) {
	t.Parallel()
	r, _ := newRepo(t)
	ctx, cancel := context.WithCancelCause(t.Context())
	errStop := errors.New("the test stops the search")
	cancel(errStop)
	if _, _, err := r.Grep(ctx, "one", 10, time.Minute); !errors.Is(err, errStop) {
		t.Errorf("Grep with a cancelled context = %v, want the cause %v", err, errStop)
	}
}

// TestSnapshotFollowsIndex checks that a Snapshot after an earlier one
// drops a file that git now ignores, and keeps a file that the user adds
// with "git add -f".
func TestSnapshotFollowsIndex(t *testing.T) {
	t.Parallel()
	r, dir := newRepo(t)
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
