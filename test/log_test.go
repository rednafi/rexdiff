package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLogPrivate checks that only the user can read the log. The log names
// the keys but not the text typed into the comment box or the find bar.
func TestLogPrivate(t *testing.T) {
	log := filepath.Join(t.TempDir(), "log")
	tm := start(t, newRepo(t), opts{args: []string{"-log", log}})
	tm.keys("]", "c")
	tm.typed("hunter2")
	tm.keys("enter")
	tm.waitFor("💬 hunter2")
	tm.keys("/")
	tm.waitFor("no preview")
	tm.typed("swordfish")
	tm.waitFor("› swordfish", "no matches")
	tm.keys("esc")
	tm.waitGone("› swordfish")
	tm.keys("q", "Q")
	if err := tm.exited(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(log)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("the log has mode %v, want -rw-------", perm)
	}
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{"key=]", "key=c", "key=enter", "key=text"} {
		if !strings.Contains(s, want) {
			t.Errorf("the log has no %q. It holds:\n%s", want, s)
		}
	}
	for _, secret := range []string{"key=h", "key=2", "key=s", "key=w"} {
		if strings.Contains(s, secret+" ") {
			t.Errorf("the log has %q, a key typed as text. It holds:\n%s", secret, s)
		}
	}
}
