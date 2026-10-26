package editor_test

import (
	"testing"

	"github.com/rednafi/rexdiff/internal/editor"
)

// TestCommands checks the command and the kind of each editor.
func TestCommands(t *testing.T) {
	t.Parallel()
	const abs = "/r/a b.go"
	tests := []struct {
		template, editor string
		script           string
		window           bool
	}{
		// The editor of the settings gets the placeholders.
		{"/opt/homebrew/bin/nvim +{line} {file}", "", `/opt/homebrew/bin/nvim +12 '/r/a b.go'`, false},
		{"code -g {file}:{line}", "", `code -g '/r/a b.go':12`, true},
		{"zed {file}:{line}", "", `zed '/r/a b.go':12`, true},
		{"open -a Zed {file}", "", `open -a Zed '/r/a b.go'`, true},
		{"myeditor --at {line}", "", `myeditor --at 12 '/r/a b.go'`, false},
		// The editor of the environment gets the line as it expects it.
		{"", "nvim", `nvim +12 '/r/a b.go'`, false},
		{"", "/usr/bin/vim -u NONE", `/usr/bin/vim -u NONE +12 '/r/a b.go'`, false},
		{"", "micro", `micro +12 '/r/a b.go'`, false},
		{"", "hx", `hx '/r/a b.go:12'`, false},
		{"", "fresh", `fresh '/r/a b.go:12'`, false},
		{"", "code --wait", `code --wait -g '/r/a b.go:12'`, true},
		{"", "subl", `subl '/r/a b.go:12'`, true},
		{"", "idea", `idea --line 12 '/r/a b.go'`, true},
		{"", "mvim", `mvim +12 '/r/a b.go'`, true},
		// A path in quotes can have spaces.
		{"", `"/Apps/VS Code.app/bin/code" -w`, `"/Apps/VS Code.app/bin/code" -w -g '/r/a b.go:12'`, true},
		{"", `'/my apps/nvim'`, `'/my apps/nvim' +12 '/r/a b.go'`, false},
		// An unknown editor gets +line, and draws in the terminal.
		{"", "vim.basic", `vim.basic +12 '/r/a b.go'`, false},
		// A wait flag tells that an unknown editor has a window.
		{"", "myeditor --wait", `myeditor --wait +12 '/r/a b.go'`, true},
	}
	for _, tt := range tests {
		got, err := editor.Resolve(editor.Sources{Template: tt.template, Editor: tt.editor}, abs, 12)
		if err != nil || got.Script != tt.script || got.Window != tt.window {
			t.Errorf("editor %q%q = %+v, %v, want script %q, window %v",
				tt.template, tt.editor, got, err, tt.script, tt.window)
		}
	}
}

// TestCommandProgram checks the program of a command. An empty word in
// quotes names no program.
func TestCommandProgram(t *testing.T) {
	t.Parallel()
	tests := []struct{ command, want string }{
		{"nvim", "nvim"},
		{"  /usr/bin/vim -u NONE", "/usr/bin/vim"},
		{`"/Apps/VS Code.app/bin/code" -w`, "/Apps/VS Code.app/bin/code"},
		{`'/my apps/nvim'`, "/my apps/nvim"},
		{`""`, ""},
		{"'' vim", ""},
	}
	const noProgram = "the editor command names no program"
	for _, tt := range tests {
		for _, c := range []struct{ template, env string }{{tt.command, ""}, {"", tt.command}} {
			got, err := editor.Resolve(editor.Sources{Template: c.template, Editor: c.env}, "/a", 1)
			switch {
			case tt.want == "" && (err == nil || err.Error() != noProgram):
				t.Errorf("Resolve(%q, %q) error = %v, want %q", c.template, c.env, err, noProgram)
			case tt.want != "" && got.Program != tt.want:
				t.Errorf("Resolve(%q, %q).Program = %q, want %q", c.template, c.env, got.Program, tt.want)
			}
		}
	}
}

// TestResolveOrder checks that the settings come first, then $VISUAL,
// $EDITOR, and core.editor. A blank value does not count.
func TestResolveOrder(t *testing.T) {
	t.Parallel()
	tests := []struct {
		template, visual, env, git string
		want                       string
	}{
		{"zed {file}", "vim", "nano", "hx", "zed"},
		{"", "vim", "nano", "hx", "vim"},
		{"", " ", "nano", "hx", "nano"},
		{"", "", "\t", "hx", "hx"},
	}
	for _, tt := range tests {
		got, err := editor.Resolve(editor.Sources{Template: tt.template, Visual: tt.visual, Editor: tt.env, Git: tt.git}, "/a", 1)
		if err != nil || got.Program != tt.want {
			t.Errorf("Resolve(%q, %q, %q, %q) = %q, %v, want %q",
				tt.template, tt.visual, tt.env, tt.git, got.Program, err, tt.want)
		}
	}
	const noEditor = `no editor: set "editor" in rexdiff.lua, $EDITOR, or git's core.editor`
	if _, err := editor.Resolve(editor.Sources{Visual: " ", Git: "\n"}, "/a", 1); err == nil || err.Error() != noEditor {
		t.Errorf("Resolve with no editor: error = %v, want %q", err, noEditor)
	}
}
