package editor_test

import (
	"testing"

	"github.com/rednafi/rexdiff/internal/editor"
)

func TestCommands(t *testing.T) {
	t.Parallel()
	const abs = "/r/a b.go"
	tests := []struct {
		template, editor string
		script           string
		window           bool
	}{
		{template: "/opt/homebrew/bin/nvim +{line} {file}", script: `/opt/homebrew/bin/nvim +12 '/r/a b.go'`},
		{template: "code -g {file}:{line}", script: `code -g '/r/a b.go':12`, window: true},
		{template: "zed {file}:{line}", script: `zed '/r/a b.go':12`, window: true},
		{template: "open -a Zed {file}", script: `open -a Zed '/r/a b.go'`, window: true},
		{template: "myeditor --at {line}", script: `myeditor --at 12 '/r/a b.go'`},
		{editor: "nvim", script: `nvim +12 '/r/a b.go'`},
		{editor: "/usr/bin/vim -u NONE", script: `/usr/bin/vim -u NONE +12 '/r/a b.go'`},
		{editor: "micro", script: `micro +12 '/r/a b.go'`},
		{editor: "hx", script: `hx '/r/a b.go:12'`},
		{editor: "fresh", script: `fresh '/r/a b.go:12'`},
		{editor: "code --wait", script: `code --wait -g '/r/a b.go:12'`, window: true},
		{editor: "subl", script: `subl '/r/a b.go:12'`, window: true},
		{editor: "idea", script: `idea --line 12 '/r/a b.go'`, window: true},
		{editor: "mvim", script: `mvim +12 '/r/a b.go'`, window: true},
		{editor: `"/Apps/VS Code.app/bin/code" -w`, script: `"/Apps/VS Code.app/bin/code" -w -g '/r/a b.go:12'`, window: true},
		{editor: `'/my apps/nvim'`, script: `'/my apps/nvim' +12 '/r/a b.go'`},
		{editor: "vim.basic", script: `vim.basic +12 '/r/a b.go'`},
		{editor: "myeditor --wait", script: `myeditor --wait +12 '/r/a b.go'`, window: true},
	}
	for _, tt := range tests {
		got, err := editor.CommandFor(editor.Sources{Template: tt.template, Editor: tt.editor}, abs, 12)
		if err != nil || got.Script != tt.script || got.Window != tt.window {
			t.Errorf("CommandFor(%q, %q) = %+v, %v, want script %q, window %v",
				tt.template, tt.editor, got, err, tt.script, tt.window)
		}
	}
}

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
			got, err := editor.CommandFor(editor.Sources{Template: c.template, Editor: c.env}, "/a", 1)
			switch {
			case tt.want == "" && (err == nil || err.Error() != noProgram):
				t.Errorf("CommandFor(%q, %q) error = %v, want %q", c.template, c.env, err, noProgram)
			case tt.want != "" && got.Program != tt.want:
				t.Errorf("CommandFor(%q, %q).Program = %q, want %q", c.template, c.env, got.Program, tt.want)
			}
		}
	}
}

func TestCommandForOrder(t *testing.T) {
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
		got, err := editor.CommandFor(editor.Sources{Template: tt.template, Visual: tt.visual, Editor: tt.env, Git: tt.git}, "/a", 1)
		if err != nil || got.Program != tt.want {
			t.Errorf("CommandFor(%q, %q, %q, %q) = %q, %v, want %q",
				tt.template, tt.visual, tt.env, tt.git, got.Program, err, tt.want)
		}
	}
	const noEditor = `no editor: set "editor" in rexdiff.lua, $EDITOR, or git's core.editor`
	if _, err := editor.CommandFor(editor.Sources{Visual: " ", Git: "\n"}, "/a", 1); err == nil || err.Error() != noEditor {
		t.Errorf("CommandFor with no editor: error = %v, want %q", err, noEditor)
	}
}
