package vcs_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rednafi/rexdiff/internal/testgit"
	"github.com/rednafi/rexdiff/vcs"
)

// newWatcher returns a Watcher of r after its first call. Its clock does
// not move, so no call waits for the pause.
func newWatcher(t *testing.T, r *vcs.Repo) *vcs.Watcher {
	t.Helper()
	w, err := r.Watch(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	w.SetClock(func() time.Time { return time.Time{} })
	if ch := changed(t, w); ch != (vcs.Change{Files: true, Refs: true}) {
		t.Fatalf("first call: %+v, want both", ch)
	}
	return w
}

// changed returns the change that w sees.
func changed(t *testing.T, w *vcs.Watcher) vcs.Change {
	t.Helper()
	ch, err := w.Changed(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return ch
}

// TestWatcher checks the changes that a Watcher sees and does not see. A
// change must have each part of want. No change must be none.
func TestWatcher(t *testing.T) {
	t.Parallel()
	files := vcs.Change{Files: true}
	tests := []struct {
		name string
		edit func(t *testing.T, dir string)
		want vcs.Change
	}{
		{"nothing", func(*testing.T, string) {}, vcs.Change{}},
		{"same size, new time", func(t *testing.T, dir string) {
			t.Helper()
			testgit.WriteFile(t, dir, "a.txt", "one\nxyz\n") // the same size
			p := filepath.Join(dir, "a.txt")
			later := time.Now().Add(time.Hour)
			if err := os.Chtimes(p, later, later); err != nil {
				t.Fatal(err)
			}
		}, files},
		{"new file", func(t *testing.T, dir string) {
			t.Helper()
			testgit.WriteFile(t, dir, "c.txt", "c\n")
		}, files},
		{"new directory", func(t *testing.T, dir string) {
			t.Helper()
			testgit.WriteFile(t, dir, "x/y/c.txt", "c\n")
		}, files},
		{"ignored", func(t *testing.T, dir string) {
			t.Helper()
			testgit.WriteFile(t, dir, "build/more", "x\n")
		}, vcs.Change{}},
		{"deleted", func(t *testing.T, dir string) {
			t.Helper()
			if err := os.Remove(filepath.Join(dir, "b.txt")); err != nil {
				t.Fatal(err)
			}
		}, files},
		{"commit", func(t *testing.T, dir string) {
			t.Helper()
			testgit.Git(t, dir, "commit", "-qam", "c")
		}, vcs.Change{Refs: true}},
		{"checkout", func(t *testing.T, dir string) {
			t.Helper()
			testgit.Git(t, dir, "checkout", "-qb", "other")
		}, vcs.Change{Refs: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r, dir := newRepo(t)
			w := newWatcher(t, r)
			tt.edit(t, dir)
			got := changed(t, w)
			if tt.want.Files && !got.Files || tt.want.Refs && !got.Refs ||
				tt.want == (vcs.Change{}) && got != tt.want {
				t.Errorf("after the edit: %+v, want %+v", got, tt.want)
			}
			if got := changed(t, w); got != (vcs.Change{}) {
				t.Errorf("second call: %+v, want none", got)
			}
		})
	}
}

// TestWatcherLastWrite checks that LastWrite sees a new file after the
// start of the watch, and that the change still shows in Changed.
func TestWatcherLastWrite(t *testing.T) {
	t.Parallel()
	r, dir := newRepo(t)
	w := newWatcher(t, r)
	if got := w.LastWrite(); !got.IsZero() {
		t.Errorf("LastWrite before a write = %v, want zero", got)
	}
	testgit.WriteFile(t, dir, "c.txt", "c\n")
	// The clock of the file system can be behind time.Now. So the test sets
	// the times of the new file and of its directory to an hour after now.
	// This is clearly after the start of the watch.
	later := time.Now().Add(time.Hour)
	for _, p := range []string{filepath.Join(dir, "c.txt"), dir} {
		if err := os.Chtimes(p, later, later); err != nil {
			t.Fatal(err)
		}
	}
	if w.LastWrite().IsZero() {
		t.Error("LastWrite after a write is zero")
	}
	if got := changed(t, w); !got.Files {
		t.Errorf("Changed after LastWrite: %+v, want Files", got)
	}
}

// TestWatcherNewDirectory checks that an edit in a new untracked directory
// is a change.
func TestWatcherNewDirectory(t *testing.T) {
	t.Parallel()
	r, dir := newRepo(t)
	w := newWatcher(t, r)
	testgit.WriteFile(t, dir, "x/c.txt", "c\n")
	changed(t, w)
	testgit.WriteFile(t, dir, "x/c.txt", "c\nd\n")
	if got := changed(t, w); !got.Files {
		t.Errorf("after an edit in x: %+v, want Files", got)
	}
}

// TestWatcherUnignore checks that a file shows after a .gitignore edit
// stops git from ignoring it.
func TestWatcherUnignore(t *testing.T) {
	t.Parallel()
	r, dir := newRepo(t)
	w := newWatcher(t, r)
	testgit.WriteFile(t, dir, "ignored.txt", "xx\n")
	if got := changed(t, w); got.Files {
		t.Errorf("after an edit of an ignored file: %+v, want no Files", got)
	}
	testgit.WriteFile(t, dir, ".gitignore", "build/\n")
	if got := changed(t, w); !got.Files {
		t.Errorf("after the .gitignore edit: %+v, want Files", got)
	}
	testgit.WriteFile(t, dir, "ignored.txt", "xxx\n")
	if got := changed(t, w); !got.Files {
		t.Errorf("after an edit of the file: %+v, want Files", got)
	}
}

// TestWatcherShown checks that a file that an expanded ignored directory
// gets or loses is a change. An edit of a shown ignored file is also a
// change, as in herdr-reviewr.
func TestWatcherShown(t *testing.T) {
	t.Parallel()
	r, dir := newRepo(t)
	w := newWatcher(t, r)
	testgit.WriteFile(t, dir, "build/new", "x\n")
	if got := changed(t, w); got.Files {
		t.Errorf("a new file in a hidden build: %+v, want no Files", got)
	}
	w.Show([]string{"build"})
	if got := changed(t, w); got.Files {
		t.Errorf("after Show: %+v, want no Files", got)
	}
	testgit.WriteFile(t, dir, "build/more", "x\n")
	if got := changed(t, w); !got.Files {
		t.Errorf("a new file in a shown build: %+v, want Files", got)
	}
	if err := os.Remove(filepath.Join(dir, "build", "more")); err != nil {
		t.Fatal(err)
	}
	if got := changed(t, w); !got.Files {
		t.Errorf("a removed file in a shown build: %+v, want Files", got)
	}
	w.Show(nil)
	testgit.WriteFile(t, dir, "build/more", "x\n")
	if got := changed(t, w); got.Files {
		t.Errorf("a new file after Show(nil): %+v, want no Files", got)
	}
	w.Show([]string{"ignored.txt"})
	testgit.WriteFile(t, dir, "ignored.txt", "an edit\n")
	if got := changed(t, w); !got.Files {
		t.Errorf("an edit of a shown file: %+v, want Files", got)
	}
	if err := os.Symlink("ignored.txt", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	w.Show([]string{"link"})
	changed(t, w)
	testgit.WriteFile(t, dir, "ignored.txt", "one more edit\n")
	if got := changed(t, w); !got.Files {
		t.Errorf("an edit of the target of a shown link: %+v, want Files", got)
	}
}

// TestWatcherNested checks that a commit in a nested repository or in a
// submodule is a change.
func TestWatcherNested(t *testing.T) {
	t.Parallel()
	for _, sub := range []bool{false, true} {
		name := map[bool]string{false: "nested", true: "submodule"}[sub]
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r, dir := newRepo(t)
			other := t.TempDir()
			testgit.InitRepo(t, other)
			testgit.WriteFile(t, other, "f", "1\n")
			testgit.Git(t, other, "add", ".")
			testgit.Git(t, other, "commit", "-qm", "1")
			inner := filepath.Join(dir, "inner")
			if sub {
				testgit.Git(t, dir, "-c", "protocol.file.allow=always",
					"submodule", "add", "-q", other, "inner")
			} else {
				testgit.Git(t, dir, "clone", "-q", other, "inner")
			}
			w := newWatcher(t, r)
			testgit.WriteFile(t, inner, "f", "2\n")
			testgit.Git(t, inner, "commit", "-qam", "2")
			if got := changed(t, w); !got.Files {
				t.Errorf("after a commit in %s: %+v, want Files", name, got)
			}
		})
	}
}

// TestWatcherRunsNoGit checks that a Watcher runs no git process while
// nothing changes.
func TestWatcherRunsNoGit(t *testing.T) {
	t.Parallel()
	env, link := gitLink(t)
	r, _ := newRepoEnv(t, env)
	w := newWatcher(t, r)
	removeGit(t, link)
	for range 3 {
		ch, err := w.Changed(t.Context())
		if err != nil || ch != (vcs.Change{}) {
			t.Fatalf("idle call: %+v, %v, want none, nil", ch, err)
		}
	}
}

// TestWatcherBudget checks that a call waits 20 times as long as the last
// call took.
func TestWatcherBudget(t *testing.T) {
	t.Parallel()
	r, dir := newRepo(t)
	w, err := r.Watch(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	// Each reading of the clock moves it by 10 ms, so each call takes 10
	// ms and the next call waits 200 ms.
	now := time.Now()
	w.SetClock(func() time.Time {
		now = now.Add(10 * time.Millisecond)
		return now
	})
	changed(t, w)
	testgit.WriteFile(t, dir, "c.txt", "c\n")
	if got := changed(t, w); got != (vcs.Change{}) {
		t.Errorf("call in the pause: %+v, want none", got)
	}
	now = now.Add(200 * time.Millisecond)
	if got := changed(t, w); !got.Files {
		t.Errorf("call after the pause: %+v, want Files", got)
	}
}

// TestWatcherUntrackedDir checks that a new file in an untracked directory
// that git lists no file in is a change. A new file in an ignored
// directory in it is not.
func TestWatcherUntrackedDir(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, setup, edit string
		want              bool
	}{
		{"empty", "e/", "e/c.txt", true},
		{"only ignored files", "g/ignored.txt", "g/c.txt", true},
		{"empty below empty", "d/e/", "d/e/c.txt", true},
		{"ignored below untracked", "u/build/", "u/build/c.txt", false},
		// A path that starts with ":" is pathspec magic to git.
		{"colon in the name", ":(x/e/", ":(x/e/c.txt", true},
		{"colon and exclude", ":!x/e/", ":!x/e/c.txt", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r, dir := newRepo(t)
			if d, ok := strings.CutSuffix(tt.setup, "/"); ok {
				if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
					t.Fatal(err)
				}
			} else {
				testgit.WriteFile(t, dir, tt.setup, "x\n")
			}
			w := newWatcher(t, r)
			testgit.WriteFile(t, dir, tt.edit, "c\n")
			if got := changed(t, w); got.Files != tt.want {
				t.Errorf("after a new %s: %+v, want Files %v", tt.edit, got, tt.want)
			}
		})
	}
}
