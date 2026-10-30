package vcs_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rednafi/rexdiff/internal/testgit"
	"github.com/rednafi/rexdiff/vcs"
)

// newWatcher has already called Changed once. Its clock never moves, so
// the pause skips no call.
func newWatcher(t *testing.T, r *vcs.Repo) *vcs.Watcher {
	t.Helper()
	w, err := r.Watch(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	w.SetClock(func() time.Time { return time.Time{} })
	if ch := changed(t, w); ch != (vcs.Change{Files: true, Refs: true}) {
		t.Fatalf("first Changed call = %+v, want both", ch)
	}
	return w
}

func changed(t *testing.T, w *vcs.Watcher) vcs.Change {
	t.Helper()
	ch, err := w.Changed(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return ch
}

// covers treats an empty want as an exact match.
func covers(got, want vcs.Change) bool {
	if want == (vcs.Change{}) {
		return got == want
	}
	filesOK := !want.Files || got.Files
	refsOK := !want.Refs || got.Refs
	return filesOK && refsOK
}

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
			testgit.WriteFile(t, dir, "a.txt", "one\nxyz\n") // same size as before
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
			r, dir := openTestRepo(t)
			w := newWatcher(t, r)
			tt.edit(t, dir)
			got := changed(t, w)
			if !covers(got, tt.want) {
				t.Errorf("Changed after the edit = %+v, want %+v", got, tt.want)
			}
			if got := changed(t, w); got != (vcs.Change{}) {
				t.Errorf("second Changed call = %+v, want none", got)
			}
		})
	}
}

func TestWatcherLastWrite(t *testing.T) {
	t.Parallel()
	r, dir := openTestRepo(t)
	w := newWatcher(t, r)
	if got := w.LastWrite(time.Now()); !got.IsZero() {
		t.Errorf("LastWrite before a write = %v, want zero", got)
	}
	testgit.WriteFile(t, dir, "c.txt", "c\n")
	// The file system clock can lag behind time.Now, so set the times of
	// the new file and its directory to one hour from now.
	later := time.Now().Add(time.Hour)
	for _, p := range []string{filepath.Join(dir, "c.txt"), dir} {
		if err := os.Chtimes(p, later, later); err != nil {
			t.Fatal(err)
		}
	}
	if w.LastWrite(later).IsZero() {
		t.Error("LastWrite after a write is zero")
	}
	if got := w.LastWrite(later.Add(-time.Minute)); !got.IsZero() {
		t.Errorf("LastWrite before the write = %v, want zero", got)
	}
	if got := changed(t, w); !got.Files {
		t.Errorf("Changed after LastWrite = %+v, want Files", got)
	}
}

func TestWatcherNewDirectory(t *testing.T) {
	t.Parallel()
	r, dir := openTestRepo(t)
	w := newWatcher(t, r)
	testgit.WriteFile(t, dir, "x/c.txt", "c\n")
	changed(t, w)
	testgit.WriteFile(t, dir, "x/c.txt", "c\nd\n")
	if got := changed(t, w); !got.Files {
		t.Errorf("Changed after an edit in x = %+v, want Files", got)
	}
}

func TestWatcherUnignore(t *testing.T) {
	t.Parallel()
	r, dir := openTestRepo(t)
	w := newWatcher(t, r)
	testgit.WriteFile(t, dir, "ignored.txt", "xx\n")
	if got := changed(t, w); got.Files {
		t.Errorf("Changed after an edit of an ignored file = %+v, want no Files", got)
	}
	testgit.WriteFile(t, dir, ".gitignore", "build/\n")
	if got := changed(t, w); !got.Files {
		t.Errorf("Changed after the .gitignore edit = %+v, want Files", got)
	}
	testgit.WriteFile(t, dir, "ignored.txt", "xxx\n")
	if got := changed(t, w); !got.Files {
		t.Errorf("Changed after an edit of the file = %+v, want Files", got)
	}
}

func TestWatcherIncluded(t *testing.T) {
	t.Parallel()
	r, dir := openTestRepo(t)
	w := newWatcher(t, r)
	testgit.WriteFile(t, dir, "build/new", "x\n")
	if got := changed(t, w); got.Files {
		t.Errorf("Changed after a new file in a hidden build = %+v, want no Files", got)
	}
	w.Include([]string{"build"})
	if got := changed(t, w); got.Files {
		t.Errorf("Changed after Include = %+v, want no Files", got)
	}
	testgit.WriteFile(t, dir, "build/more", "x\n")
	if got := changed(t, w); !got.Files {
		t.Errorf("Changed after a new file in a shown build = %+v, want Files", got)
	}
	if err := os.Remove(filepath.Join(dir, "build", "more")); err != nil {
		t.Fatal(err)
	}
	if got := changed(t, w); !got.Files {
		t.Errorf("Changed after a removed file in a shown build = %+v, want Files", got)
	}
	w.Include(nil)
	testgit.WriteFile(t, dir, "build/more", "x\n")
	if got := changed(t, w); got.Files {
		t.Errorf("Changed after a new file after Include(nil) = %+v, want no Files", got)
	}
	w.Include([]string{"ignored.txt"})
	testgit.WriteFile(t, dir, "ignored.txt", "an edit\n")
	if got := changed(t, w); !got.Files {
		t.Errorf("Changed after an edit of a shown file = %+v, want Files", got)
	}
	if err := os.Symlink("ignored.txt", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	w.Include([]string{"link"})
	changed(t, w)
	testgit.WriteFile(t, dir, "ignored.txt", "one more edit\n")
	if got := changed(t, w); !got.Files {
		t.Errorf("Changed after an edit of the target of a shown link = %+v, want Files", got)
	}
}

func TestWatcherNested(t *testing.T) {
	t.Parallel()
	for _, sub := range []bool{false, true} {
		name := "nested"
		if sub {
			name = "submodule"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r, dir := openTestRepo(t)
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
				t.Errorf("Changed after a commit in %s = %+v, want Files", name, got)
			}
		})
	}
}

func TestWatcherRunsNoGit(t *testing.T) {
	t.Parallel()
	env, link := gitLink(t)
	r, _ := openTestRepoEnv(t, env)
	w := newWatcher(t, r)
	removeGit(t, link)
	for range 3 {
		ch, err := w.Changed(t.Context())
		if err != nil || ch != (vcs.Change{}) {
			t.Fatalf("idle Changed call = %+v, %v, want none, nil", ch, err)
		}
	}
}

func TestWatcherBudget(t *testing.T) {
	t.Parallel()
	r, dir := openTestRepo(t)
	w, err := r.Watch(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	// Each read of the clock advances it by 10 ms, so each call takes
	// 10 ms and the next check waits 1 s.
	now := time.Now()
	w.SetClock(func() time.Time {
		now = now.Add(10 * time.Millisecond)
		return now
	})
	changed(t, w)
	testgit.WriteFile(t, dir, "c.txt", "c\n")
	if got := changed(t, w); got != (vcs.Change{}) {
		t.Errorf("Changed in the pause = %+v, want none", got)
	}
	now = now.Add(time.Second)
	if got := changed(t, w); !got.Files {
		t.Errorf("Changed after the pause = %+v, want Files", got)
	}
	// A 1 s call would give a 100 s pause, but the pause is at most 30 s.
	step := time.Second
	w.SetClock(func() time.Time {
		now = now.Add(step)
		return now
	})
	changed(t, w)
	step = 0
	testgit.WriteFile(t, dir, "d.txt", "d\n")
	now = now.Add(30 * time.Second)
	if got := changed(t, w); !got.Files {
		t.Errorf("Changed after 30 s = %+v, want Files", got)
	}
}

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
		// git treats a path that starts with ":" as pathspec magic.
		{"colon in the name", ":(x/e/", ":(x/e/c.txt", true},
		{"colon and exclude", ":!x/e/", ":!x/e/c.txt", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r, dir := openTestRepo(t)
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
				t.Errorf("Changed after a new %s = %+v, want Files %v", tt.edit, got, tt.want)
			}
		})
	}
}

// TestWatcherConcurrent is useful only with -race.
func TestWatcherConcurrent(t *testing.T) {
	t.Parallel()
	r, dir := openTestRepo(t)
	for i := range 50 {
		testgit.WriteFile(t, dir, fmt.Sprintf("many/f%02d.txt", i), "x\n")
	}
	testgit.Git(t, dir, "add", ".")
	testgit.Git(t, dir, "commit", "-qm", "many")
	w := newWatcher(t, r)
	if err := os.Mkdir(filepath.Join(dir, "new"), 0o755); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Go(func() {
		for i := range 20 {
			// Each new file makes Changed list the files again.
			p := filepath.Join(dir, "new", fmt.Sprintf("n%02d.txt", i))
			if err := os.WriteFile(p, []byte("n\n"), 0o644); err != nil {
				t.Error(err)
				return
			}
			if _, err := w.Changed(t.Context()); err != nil {
				t.Error(err)
				return
			}
		}
	})
	for range 20 {
		w.Include([]string{"build"})
		w.LastWrite(time.Now())
		w.Include(nil)
	}
	wg.Wait()
	changed(t, w)
	testgit.WriteFile(t, dir, "many/f37.txt", "an edit\n")
	if got := changed(t, w); !got.Files {
		t.Errorf("Changed after an edit of many/f37.txt = %+v, want Files", got)
	}
}
