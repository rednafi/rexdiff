package clipboard_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/rednafi/rexdiff/internal/clipboard"
	"github.com/rednafi/rexdiff/internal/testgit"
)

// fakeTool writes an executable script called name to a new directory. It
// returns the directory, and an environment whose PATH contains only that
// directory.
func fakeTool(t *testing.T, name, script string) (string, []string) {
	t.Helper()
	dir := t.TempDir()
	testgit.Script(t, dir, name, script)
	return dir, []string{"PATH=" + dir}
}

// TestWrite checks that each clipboard tool receives the text on stdin and
// the expected arguments.
func TestWrite(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, want string }{
		{"pbcopy", "hi\n"},
		{"wl-copy", "hi\n"},
		{"xclip", "-selection|clipboard|hi\n"},
		{"xsel", "--clipboard|--input|hi\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// The script writes each argument followed by a bar, then its input.
			dir, env := fakeTool(t, tt.name, `{ for a; do printf '%s|' "$a"; done; /bin/cat; } > "${0%/*}/out"`+"\n")
			if err := clipboard.Write(t.Context(), env, "hi\n"); err != nil {
				t.Fatalf("Write: %v", err)
			}
			got, err := os.ReadFile(filepath.Join(dir, "out"))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.want {
				t.Errorf("Write(hi) with %s: output = %q, want %q",
					tt.name, got, tt.want)
			}
		})
	}
}

// TestWriteFails checks the errors that Write returns when no tool is
// installed and when the tool fails.
func TestWriteFails(t *testing.T) {
	t.Parallel()
	for _, env := range [][]string{nil, {"PATH=" + t.TempDir()}} {
		if err := clipboard.Write(t.Context(), env, "x"); !errors.Is(err, clipboard.ErrNoTool) {
			t.Errorf("no tool in %q: Write = %v, want ErrNoTool", env, err)
		}
	}
	_, env := fakeTool(t, "pbcopy", "exit 1\n")
	err := clipboard.Write(t.Context(), env, "x")
	if err == nil || errors.Is(err, clipboard.ErrNoTool) {
		t.Errorf("pbcopy exits 1: Write = %v, want an error that is not ErrNoTool", err)
	}
}
