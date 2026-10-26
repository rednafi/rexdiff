package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/rednafi/rexdiff/rex"
)

// TestResolveEditor checks the command and the kind of each editor.
func TestResolveEditor(t *testing.T) {
	const abs = "/r/a b.go"
	tests := []struct {
		template, value string
		script          string
		terminal        bool
	}{
		// The editor of the settings gets the placeholders.
		{"/opt/homebrew/bin/nvim +{line} {file}", "", `/opt/homebrew/bin/nvim +12 '/r/a b.go'`, true},
		{"code -g {file}:{line}", "", `code -g '/r/a b.go':12`, false},
		{"zed {file}:{line}", "", `zed '/r/a b.go':12`, false},
		{"myeditor --at {line}", "", `myeditor --at 12 '/r/a b.go'`, true},
		// The editor of the environment gets the line as it expects it.
		{"", "nvim", `nvim +12 '/r/a b.go'`, true},
		{"", "/usr/bin/vim -u NONE", `/usr/bin/vim -u NONE +12 '/r/a b.go'`, true},
		{"", "micro", `micro +12 '/r/a b.go'`, true},
		{"", "hx", `hx '/r/a b.go:12'`, true},
		{"", "fresh", `fresh '/r/a b.go:12'`, true},
		{"", "code --wait", `code --wait -g '/r/a b.go:12'`, false},
		{"", "subl", `subl '/r/a b.go:12'`, false},
		{"", "idea", `idea --line 12 '/r/a b.go'`, false},
		{"", "mvim", `mvim +12 '/r/a b.go'`, false},
		// An unknown editor gets only the file, in the terminal.
		{"", "myeditor", `myeditor '/r/a b.go'`, true},
		// A wait flag tells that an unknown editor has a window.
		{"", "myeditor --wait", `myeditor --wait '/r/a b.go'`, false},
	}
	for _, tt := range tests {
		got := resolveEditor(tt.template, tt.value, abs, 12)
		if got.script != tt.script || got.terminal != tt.terminal {
			t.Errorf("resolveEditor(%q, %q) = %q, terminal %v; want %q, terminal %v",
				tt.template, tt.value, got.script, got.terminal, tt.script, tt.terminal)
		}
	}
}

// fakeRex makes m use a rex in dir that writes its arguments to rex.log
// in dir. It returns the path of rex.log.
func fakeRex(t *testing.T, m *model, dir string) string {
	t.Helper()
	log := filepath.Join(dir, "rex.log")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + log + "'\n"
	if err := os.WriteFile(filepath.Join(dir, "rex"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REX_SESSION", "S")
	t.Setenv("REX_BLOCK", "B")
	t.Setenv("REX_BIN_DIR", dir)
	m.rex = rex.FromEnv()
	return log
}

// pressE moves the cursor to the inserted line 4 of a.go and presses e. It
// returns the command of the key.
func pressE(t *testing.T, m model) (model, tea.Cmd) {
	t.Helper()
	m = press(t, m, "j", "j", "j", "j", "j")
	next, cmd := m.Update(tea.KeyPressMsg{Code: 'e', Text: "e"})
	return *next.(*model), cmd
}

// TestTerminalEditorInRex checks that a terminal editor opens in a new Rex
// block, at the line. A terminal editor in the background does not show,
// and it takes keys from rexdiff.
func TestTerminalEditorInRex(t *testing.T) {
	tests := []struct {
		name, editor, env, want string
	}{
		{"settings", "/opt/homebrew/bin/nvim +{line} {file}", "", "sh -c /opt/homebrew/bin/nvim +4 '%s'"},
		{"environment", "", "nvim", "sh -c nvim +4 '%s'"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, _ := newTestModel(t)
			m.cfg.Editor = tt.editor
			t.Setenv("VISUAL", "")
			t.Setenv("EDITOR", tt.env)
			log := fakeRex(t, &m, t.TempDir())
			if _, cmd := pressE(t, m); cmd != nil {
				t.Errorf("e returned a command %T, want none", cmd)
			}
			b, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			abs := filepath.Join(m.repo.Root(), "a.go")
			want := "split -s S -b B --cwd " + m.repo.Root() + " -- " +
				strings.Replace(tt.want, "%s", abs, 1)
			if got := strings.TrimSpace(string(b)); got != want {
				t.Errorf("rex got\n%s\nwant\n%s", got, want)
			}
		})
	}
}

// TestWindowEditor checks that a window editor gets the file and the line,
// and runs without a terminal. So it cannot take the keys of rexdiff.
func TestWindowEditor(t *testing.T) {
	m, _ := newTestModel(t)
	bin := t.TempDir()
	out := filepath.Join(bin, "out")
	script := "#!/bin/sh\n" +
		"{ echo \"$*\"; if (exec 3</dev/tty) 2>/dev/null; then echo tty; else echo no tty; fi; } > '" + out + "'\n"
	if err := os.WriteFile(filepath.Join(bin, "code"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	m.cfg.Editor = filepath.Join(bin, "code") + " -g {file}:{line}"
	log := fakeRex(t, &m, t.TempDir())
	m, cmd := pressE(t, m)
	if cmd == nil {
		t.Fatal("e returned no command")
	}
	if msg := cmd(); msg != nil {
		t.Fatalf("the editor failed: %v", msg)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	abs := filepath.Join(m.repo.Root(), "a.go")
	if want := "-g " + abs + ":4\nno tty\n"; string(b) != want {
		t.Errorf("the editor got\n%q\nwant\n%q", b, want)
	}
	if _, err := os.Stat(log); err == nil {
		t.Error("a window editor opened a Rex block")
	}
}

// TestEditorError checks that the footer tells why an editor failed.
func TestEditorError(t *testing.T) {
	bin := t.TempDir()
	script := "#!/bin/sh\necho 'code: no such file' >&2\nexit 3\n"
	if err := os.WriteFile(filepath.Join(bin, "code"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, editor, path, want string
	}{
		{"fails", filepath.Join(bin, "code") + " -g {file}", os.Getenv("PATH"), "editor: exit status 3: code: no such file"},
		{"cannot start", "code -g {file}", "", "editor: "}, // sh is not in PATH
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, _ := newTestModel(t)
			m.cfg.Editor = tt.editor
			t.Setenv("PATH", tt.path)
			m, cmd := pressE(t, m)
			if cmd == nil {
				t.Fatal("e returned no command")
			}
			done := make(chan tea.Msg)
			go func() { done <- cmd() }()
			select {
			case msg := <-done:
				m = step(t, m, msg)
			case <-time.After(5 * time.Second):
				t.Fatal("the command did not return")
			}
			if !strings.Contains(view(m), tt.want) {
				t.Errorf("the footer does not show %q:\n%s", tt.want, view(m))
			}
		})
	}
}
