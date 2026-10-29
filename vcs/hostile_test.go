package vcs_test

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"testing/fstest"
	"time"

	"github.com/rednafi/rexdiff/internal/testgit"
	"github.com/rednafi/rexdiff/internal/xctx"
	"github.com/rednafi/rexdiff/vcs"
)

// commitAll stages and commits every change in the working tree of dir.
func commitAll(t *testing.T, dir string) {
	t.Helper()
	testgit.Git(t, dir, "add", "-A")
	testgit.Git(t, dir, "commit", "-qm", "all")
}

// TestRevIsNotAnOption checks that every method that takes a revision
// fails for a revision that starts with "-", instead of passing it to git
// as an option, and that --output does not overwrite a file.
func TestRevIsNotAnOption(t *testing.T) {
	t.Parallel()
	r, _ := openTestRepo(t)
	ctx := t.Context()
	out := filepath.Join(t.TempDir(), "out")
	testgit.WriteFile(t, filepath.Dir(out), "out", "keep\n")
	for _, rev := range []string{"--output=" + out, "--independent", "--all", "-h"} {
		if id, err := r.ResolveRev(ctx, rev); err == nil {
			t.Errorf("ResolveRev(%q) = %s, want an error", rev, id)
		}
		if id, err := r.Tree(ctx, rev); err == nil {
			t.Errorf("Tree(%q) = %s, want an error", rev, id)
		}
		if id, err := r.MergeBaseTree(ctx, rev); err == nil {
			t.Errorf("MergeBaseTree(%q) = %s, want an error", rev, id)
		}
		if id, err := r.ParentTree(ctx, rev); err == nil {
			t.Errorf("ParentTree(%q) = %s, want an error", rev, id)
		}
		if _, err := r.Diff(ctx, rev, head(t, r)); err == nil {
			t.Errorf("Diff(%q, HEAD): no error, want an error", rev)
		}
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "keep\n" {
		t.Errorf("the file of --output = %q, want %q", b, "keep\n")
	}
}

// TestHostilePaths checks that Diff, Files, ReadFile and Grep handle file
// names that look like options, globs or pathspecs, or that contain
// quotes, tabs, newlines or invalid UTF-8, without changing them.
func TestHostilePaths(t *testing.T) {
	t.Parallel()
	r, dir := openTestRepo(t)
	ctx := t.Context()
	commitAll(t, dir)
	hostile := []string{
		"-n", "--output=x", "-", "tab\tname", "new\nline", "trail\n",
		`say "hi"`, `back\slash`, "ünï cödé", " spaces ",
		"*", "[a]", ":(glob)x", "\xff\xfe",
	}
	want := map[string]string{} // the expected content of each path
	for i, p := range hostile {
		s := fmt.Sprintf("id%02d\n", i)
		if err := os.WriteFile(filepath.Join(dir, p), []byte(s), 0o644); err != nil {
			// Some file systems reject some names; macOS, for example,
			// requires valid UTF-8.
			t.Logf("skip %q: %v", p, err)
			continue
		}
		want[p] = s
	}

	got := map[string]string{}
	for _, f := range changes(t, r, head(t, r), snapshot(t, r)) {
		if f.Status != vcs.StatusAdded {
			t.Errorf("Diff: %q has status %c, want A", f.Path, f.Status)
		}
		got[f.Path] = f.NewText
	}
	if !maps.Equal(got, want) {
		t.Errorf("Diff = \n%q\nwant:\n%q", got, want)
	}

	es, err := r.Files(ctx)
	if err != nil {
		t.Fatal(err)
	}
	files := paths(es)
	for p, s := range want {
		if !slices.Contains(files, p) {
			t.Errorf("Files has no %q, want it", p)
		}
		if got, err := r.ReadFile(p); got != s || err != nil {
			t.Errorf("ReadFile(%q) = %q, %v, want %q", p, got, err, s)
		}
		q := strings.TrimSpace(s)
		if ms, _, err := r.Grep(ctx, q, 10, time.Minute); err != nil || len(ms) != 1 || ms[0].Path != p {
			t.Errorf("Grep(%q) = %+v, %v, want one match in %q", q, ms, err, p)
		}
	}
}

// TestDiffFileKinds checks what Diff returns for symbolic links, mode
// changes, a file replaced by a directory or by a link, CRLF line endings,
// a missing final newline, and empty files.
func TestDiffFileKinds(t *testing.T) {
	t.Parallel()
	r, dir := openTestRepo(t)
	testgit.Git(t, dir, "config", "core.autocrlf", "false")
	testgit.WriteFile(t, dir, "mode.sh", "echo\n")
	testgit.WriteFile(t, dir, "dir", "a file\n")
	testgit.WriteFile(t, dir, "type", "a file\n")
	commitAll(t, dir)

	outside := t.TempDir()
	link := func(target, name string) {
		t.Helper()
		if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	link("a.txt", "link")
	link(outside, "out")
	if err := os.Chmod(filepath.Join(dir, "mode.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "dir")); err != nil {
		t.Fatal(err)
	}
	testgit.WriteFile(t, dir, "dir/in", "in\n")
	if err := os.Remove(filepath.Join(dir, "type")); err != nil {
		t.Fatal(err)
	}
	link("a.txt", "type")
	err := os.CopyFS(dir, fstest.MapFS{
		"crlf":       {Data: []byte("a\r\nb\r\n")},
		"empty":      {},
		"no-newline": {Data: []byte("end")},
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []vcs.File{
		{Path: "crlf", Status: vcs.StatusAdded, NewText: "a\r\nb\r\n"},
		{Path: "dir", Status: vcs.StatusDeleted, OldText: "a file\n"},
		{Path: "dir/in", Status: vcs.StatusAdded, NewText: "in\n"},
		{Path: "empty", Status: vcs.StatusAdded},
		// git stores a symbolic link as its target path.
		{Path: "link", Status: vcs.StatusAdded, NewText: "a.txt"},
		{Path: "mode.sh", Status: vcs.StatusModified, OldText: "echo\n", NewText: "echo\n"},
		{Path: "no-newline", Status: vcs.StatusAdded, NewText: "end"},
		{Path: "out", Status: vcs.StatusAdded, NewText: outside},
		{Path: "type", Status: vcs.StatusModified, OldText: "a file\n", NewText: "a.txt"},
	}
	if got := changes(t, r, head(t, r), snapshot(t, r)); !slices.Equal(got, want) {
		t.Errorf("Diff = \n%+v\nwant:\n%+v", got, want)
	}
}

// TestSnapshotNestedRepo checks that SnapshotTree still adds the other
// files when git cannot add a nested repository with no commits, and adds
// a nested repository with a commit as a submodule.
func TestSnapshotNestedRepo(t *testing.T) {
	t.Parallel()
	r, dir := openTestRepo(t)
	commitAll(t, dir)
	testgit.InitRepo(t, filepath.Join(dir, "empty"))
	testgit.WriteFile(t, dir, "empty/x", "x\n")
	testgit.WriteFile(t, dir, "full/y", "y\n")
	testgit.InitRepo(t, filepath.Join(dir, "full"))
	commitAll(t, filepath.Join(dir, "full"))
	testgit.WriteFile(t, dir, "z.txt", "z\n")

	got := changes(t, r, head(t, r), snapshot(t, r))
	if names(got) != "Afull Az.txt" || !strings.HasPrefix(got[0].NewText, "Subproject commit ") {
		t.Errorf("Diff = %+v, want full as a submodule and z.txt", got)
	}
}

// TestReadFileStaysInRoot checks that ReadFile refuses paths and symbolic
// links that lead outside the root, devices, directories and named pipes,
// but follows a link to a regular file inside the root.
func TestReadFileStaysInRoot(t *testing.T) {
	t.Parallel()
	// A repository can contain a link to a secret file, or to a device
	// that never ends, such as /dev/zero.
	r, dir := openTestRepo(t)
	secret := filepath.Join(filepath.Dir(dir), "secret")
	testgit.WriteFile(t, filepath.Dir(dir), "secret", "secret\n")
	links := map[string]string{
		"to-secret": secret,
		"to-zero":   "/dev/zero",
		"to-dir":    "build",
	}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "fifo"), 0o644); err != nil {
		t.Fatal(err)
	}
	bad := []string{"../secret", secret, "to-secret", "to-zero", "to-dir", "fifo"}
	for _, p := range bad {
		if s, err := r.ReadFile(p); err == nil {
			t.Errorf("ReadFile(%q) = %q, want an error", p, s)
		}
	}
	if err := os.Symlink("a.txt", filepath.Join(dir, "to-a")); err != nil {
		t.Fatal(err)
	}
	if s, err := r.ReadFile("to-a"); s != "one\ntwo\n" || err != nil {
		t.Errorf("ReadFile(to-a) = %q, %v, want the content of a.txt", s, err)
	}
}

// TestGitWorktree checks Diff and SnapshotTree in a working tree created
// by "git worktree add", and that the snapshot does not change what git
// status reports there.
func TestGitWorktree(t *testing.T) {
	t.Parallel()
	// Such a working tree has its own index, which SnapshotTree must not
	// change.
	_, dir := openTestRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	testgit.Git(t, dir, "worktree", "add", "-q", "-b", "other", wt, "main")
	testgit.WriteFile(t, wt, "a.txt", "changed\n")
	testgit.WriteFile(t, wt, "staged.txt", "s\n")
	testgit.Git(t, wt, "add", "staged.txt")

	r := open(t, wt)
	if got := uncommitted(t, r); got != "Ma.txt Astaged.txt" {
		t.Errorf("Diff(HEAD, Snapshot) = %s, want Ma.txt Astaged.txt", got)
	}
	st := testgit.Git(t, wt, "status", "--porcelain")
	if st != " M a.txt\nA  staged.txt\n" {
		t.Errorf("git status in the worktree = \n%s\nwant:\n%s",
			st, " M a.txt\nA  staged.txt\n")
	}
}

// TestDetachedHead checks Diff, Commits, and Branch when HEAD is detached.
func TestDetachedHead(t *testing.T) {
	t.Parallel()
	r, dir := openTestRepo(t)
	ctx := t.Context()
	testgit.Git(t, dir, "checkout", "-q", "--detach", "main")
	if got := uncommitted(t, r); got != "A.gitignore Ma.txt Anew.txt" {
		t.Errorf("Diff(HEAD, Snapshot) = %s, want A.gitignore Ma.txt Anew.txt",
			got)
	}
	if cs, err := r.Commits(ctx, ""); err != nil || len(cs) != 1 || cs[0].Subject != "init" {
		t.Errorf("Commits = %+v, %v, want init", cs, err)
	}
	if b, err := r.Branch(ctx); err != nil || b != "HEAD" {
		t.Errorf("Branch = %q, %v, want HEAD, nil", b, err)
	}
}

// TestGrepIsLiteral checks that Grep searches for the query as literal
// text, not as a pattern or an option, and that it ignores case only when
// the query has no upper case letter.
func TestGrepIsLiteral(t *testing.T) {
	t.Parallel()
	r, dir := openTestRepo(t)
	ctx := t.Context()
	testgit.WriteFile(t, dir, "g.txt", "abc\na.c\n-v\n--help\n[x\n(y\n\\d\n*\n^a\nx$\nAbc\n")
	// The grep.column setting adds a field to the output, which Grep must
	// turn off.
	testgit.Git(t, dir, "config", "grep.column", "true")
	tests := []struct {
		q    string
		want []string
	}{
		{"a.c", []string{"a.c"}},
		{"-v", []string{"-v"}},
		{"--help", []string{"--help"}},
		{"[x", []string{"[x"}},
		{"(y", []string{"(y"}},
		{`\d`, []string{`\d`}},
		{"*", []string{"*"}},
		{"^a", []string{"^a"}},
		{"x$", []string{"x$"}},
		{"abc\na.c", nil}, // a single line cannot contain a newline
		{"abc", []string{"abc", "Abc"}},
		{"Abc", []string{"Abc"}},
	}
	for _, tt := range tests {
		ms, _, err := r.Grep(ctx, tt.q, 10, time.Minute)
		if err != nil {
			t.Errorf("Grep(%q) = %v, want no error", tt.q, err)
			continue
		}
		var got []string
		for _, m := range ms {
			got = append(got, m.Text)
		}
		if !slices.Equal(got, tt.want) {
			t.Errorf("Grep(%q) = %q, want %q", tt.q, got, tt.want)
		}
	}
}

// TestCommitsHostileSubject checks that Commits keeps every byte of a
// commit subject, including the field separator that Commits uses and
// escape sequences.
func TestCommitsHostileSubject(t *testing.T) {
	t.Parallel()
	r, dir := openTestRepo(t)
	subjects := []string{"a\x1fb", "\x1b[31mred\x1b[0m", "tab\there"}
	for _, s := range subjects {
		testgit.Git(t, dir, "commit", "-q", "--allow-empty", "-m", s)
	}
	cs, err := r.Commits(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range cs {
		got = append(got, c.Subject)
		if c.Author != "t" {
			t.Errorf("Author of %q = %q, want t", c.Subject, c.Author)
		}
	}
	want := []string{subjects[2], subjects[1], subjects[0], "b", "init"}
	if !slices.Equal(got, want) {
		t.Errorf("Commits subjects = %q, want %q", got, want)
	}
}

// TestPRBadJSON checks how PR handles gh output that is not a normal pull
// request: an empty object, text that is not JSON, a number out of range,
// and null fields.
func TestPRBadJSON(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, out string
		wantErr   error // the error to match, or nil to accept any error
		ok        bool  // true if PR must succeed
	}{
		{name: "empty object", out: `{}`, wantErr: vcs.ErrNoPR},
		{name: "not JSON", out: `warning: no pull request`},
		{name: "huge number", out: `{"title": "T", "number": 1e30}`},
		{
			name: "null fields",
			out: `{"title": "T", "author": null, "comments": null, ` +
				`"statusCheckRollup": null}`,
			ok: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			bin := t.TempDir()
			fakeTool(t, bin, "gh", map[string]string{ghArgs: tt.out})
			r, _ := repoAt(t, testgit.ToolEnv(t, bin), "https://github.com/o/r")
			pr, err := r.PR(t.Context(), vcs.Hosts{})
			switch {
			case tt.ok && (err != nil || pr.Title != "T"):
				t.Errorf("PR = %+v, %v, want the title T", pr, err)
			case !tt.ok && err == nil:
				t.Errorf("PR = %+v, want an error", pr)
			case tt.wantErr != nil && !errors.Is(err, tt.wantErr):
				t.Errorf("PR: err = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

// TestGrepDeadline checks that when the budget or the deadline of ctx
// passes, Grep stops git, returns the matches found so far without the
// incomplete one, and sets more.
func TestGrepDeadline(t *testing.T) {
	t.Parallel()
	env, link := gitLink(t)
	r, _ := openTestRepoEnv(t, env)
	// After Open, a fake git replaces the real one. It prints one match and
	// an incomplete record, then hangs. If a file named slow is next to it,
	// it first sleeps for much longer than the budget. If a FIFO named
	// block is next to it, it waits on the FIFO and prints nothing.
	removeGit(t, link)
	bin := filepath.Dir(link)
	testgit.Script(t, bin, "git", `d="${0%/*}"
[ -e "$d/slow" ] && /bin/sleep 0.3
[ -p "$d/block" ] && read -r _ < "$d/block"
printf 'a.go\0001\000hit\nb.go\00012'
exec /bin/sleep 10
`)
	grep := func(timeout, budget time.Duration) ([]vcs.Match, bool, error) {
		ctx, cancel := xctx.WithTimeout(t.Context(), timeout, "test grep")
		defer cancel()
		return r.Grep(ctx, "hit", 10, budget)
	}
	if ms, more, err := grep(time.Minute, 50*time.Millisecond); err != nil || !more ||
		len(ms) != 1 || ms[0].Text != "hit" {
		t.Errorf("Grep past the budget = %+v, %v, %v, want the hit, more, no error", ms, more, err)
	}
	testgit.WriteFile(t, bin, "slow", "")
	if ms, more, err := grep(time.Minute, time.Millisecond); err != nil || !more || len(ms) != 1 {
		t.Errorf("Grep with no line at the budget = %+v, %v, %v, want the first hit, more", ms, more, err)
	}
	if err := os.Remove(filepath.Join(bin, "slow")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(bin, "block"), 0o644); err != nil {
		t.Fatal(err)
	}
	if ms, more, err := grep(200*time.Millisecond, time.Millisecond); err != nil || !more || len(ms) != 0 {
		t.Errorf("Grep past the deadline = %+v, %v, %v, want none, more, no error", ms, more, err)
	}
}

// TestSnapshotWaitStops checks that a SnapshotTree call that is waiting
// for another call returns the cause of its context when its context
// ends, and that the running call returns its own cause when cancelled.
func TestSnapshotWaitStops(t *testing.T) {
	t.Parallel()
	env, link := gitLink(t)
	r, _ := openTestRepoEnv(t, env)
	// After Open, a fake git replaces the real one. It writes to the FIFO
	// named started, which tells the test that the first SnapshotTree holds
	// the lock, and then waits on the FIFO named block.
	removeGit(t, link)
	bin := filepath.Dir(link)
	for _, name := range []string{"started", "block"} {
		if err := syscall.Mkfifo(filepath.Join(bin, name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	testgit.Script(t, bin, "git", `d="${0%/*}"
echo started > "$d/started"
read -r _ < "$d/block"
`)
	errFirst := errors.New("the test stops the first snapshot")
	ctx, cancel := context.WithCancelCause(t.Context())
	defer cancel(fmt.Errorf("test: done: %w", context.Canceled))
	done := make(chan error, 1)
	go func() {
		_, err := r.SnapshotTree(ctx)
		done <- err
	}()
	if _, err := os.ReadFile(filepath.Join(bin, "started")); err != nil {
		t.Fatal(err)
	}

	ctx2, cancel2 := xctx.WithTimeout(t.Context(), 50*time.Millisecond, "second snapshot")
	defer cancel2()
	_, err := r.SnapshotTree(ctx2)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Snapshot that waits = %v, want the cause of its context", err)
	}

	cancel(errFirst)
	if err := <-done; !errors.Is(err, errFirst) {
		t.Errorf("Snapshot that holds the lock = %v, want the cause %v", err, errFirst)
	}
}
