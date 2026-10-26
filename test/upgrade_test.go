package test

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestUpgradeAtStart checks that rexdiff in Rex writes a changed
// rexdiff_toggle.lua again, keeps rexdiff.lua, and reloads Rex. It does not
// change a rexdiff_toggle.lua that runs another rexdiff. Without
// rexdiff_toggle.lua, it writes no file.
func TestUpgradeAtStart(t *testing.T) {
	repo := newRepo(t)
	rex := newRex(t, repo)
	config := filepath.Join(t.TempDir(), "init.lua")
	env := []string{"REX_CONFIG=" + config}
	install := exec.CommandContext(t.Context(), binary(t), "install")
	install.Env = slices.Concat(cleanEnv(), env, []string{"REX_BIN_DIR=" + rex.dir})
	if out, err := install.CombinedOutput(); err != nil {
		t.Fatalf("rexdiff install: %v\n%s", err, out)
	}
	lua := filepath.Join(filepath.Dir(config), "rexdiff.lua")
	code := filepath.Join(filepath.Dir(config), "rexdiff_toggle.lua")
	edited := strings.Replace(read(t, lua), "local settings = {", "local settings = { layout = \"split\",", 1)
	write(t, lua, edited)
	installed := read(t, code)
	write(t, code, strings.Replace(installed, "local function must", "local function old_must", 1))

	tm := start(t, repo, opts{rex: rex, env: env})
	tm.keys("q")
	if err := tm.exited(); err != nil {
		t.Fatal(err)
	}
	if got := read(t, code); got != installed {
		t.Errorf("rexdiff_toggle.lua after the start = \n%s\nwant:\n%s", got, installed)
	}
	if got := read(t, lua); got != edited {
		t.Errorf("rexdiff.lua after the start = \n%s\nwant:\n%s", got, edited)
	}
	// The install reloads once, and the upgrade once.
	if n := strings.Count(rex.calls(t), "config reload"); n != 2 {
		t.Errorf("config reloads = %d, want 2", n)
	}

	other := strings.Replace(installed, "local BIN = ", "local BIN = \"/other/rexdiff\" -- ", 1)
	write(t, code, other)
	tm = start(t, repo, opts{rex: rex, env: env})
	tm.keys("q")
	if err := tm.exited(); err != nil {
		t.Fatal(err)
	}
	if got := read(t, code); got != other {
		t.Errorf("rexdiff_toggle.lua of another rexdiff after the start = \n%s\nwant:\n%s", got, other)
	}

	if err := os.Remove(code); err != nil {
		t.Fatal(err)
	}
	tm = start(t, repo, opts{rex: rex, env: env})
	tm.keys("q")
	if err := tm.exited(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(code); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("rexdiff_toggle.lua after a start without it: %v, want no file", err)
	}
}

// TestUpgradeFrom027 checks the first start of rexdiff in Rex after an
// upgrade from rexdiff 0.2.7. The rexdiff.lua of 0.2.7 has the settings,
// then the code under a marker line. rexdiff keeps the settings, puts the
// loader in place of the code, writes rexdiff_toggle.lua, and reloads Rex.
func TestUpgradeFrom027(t *testing.T) {
	repo := newRepo(t)
	rex := newRex(t, repo)
	dir := t.TempDir()
	config := filepath.Join(dir, "init.lua")
	write(t, config, "require(\"rexdiff\")\n")
	lua := filepath.Join(dir, "rexdiff.lua")
	old := strings.Replace(read(t, filepath.Join("testdata", "rexdiff-0.2.7.lua")),
		"{{BIN}}", binary(t), 1)
	old = strings.Replace(old, "  -- layout = \"split\",", "  layout = \"split\",", 1)
	write(t, lua, old)
	settings, _, _ := strings.Cut(old, "-- rexdiff code below.")

	tm := start(t, repo, opts{rex: rex, env: []string{"REX_CONFIG=" + config}})
	tm.keys("q")
	if err := tm.exited(); err != nil {
		t.Fatal(err)
	}
	got := read(t, lua)
	if !strings.HasPrefix(got, strings.TrimRight(settings, "\n")) ||
		!strings.Contains(got, `require("rexdiff_toggle")(settings)`) ||
		strings.Contains(got, "rexdiff code below") {
		t.Errorf("rexdiff.lua after the start = \n%s\nwant the settings of 0.2.7 and the loader", got)
	}
	if got := read(t, filepath.Join(dir, "rexdiff_toggle.lua")); !strings.Contains(got, "local BIN = \""+binary(t)+"\"") {
		t.Errorf("rexdiff_toggle.lua after the start = \n%s\nwant the code for %s", got, binary(t))
	}
	if n := strings.Count(rex.calls(t), "config reload"); n != 1 {
		t.Errorf("config reloads = %d, want 1", n)
	}
	if bin := os.Getenv("REXDIFF_REX"); bin != "" {
		check := exec.CommandContext(t.Context(), bin, "config", "check")
		check.Env = append(os.Environ(), "REX_CONFIG="+config)
		out, err := check.CombinedOutput()
		if err != nil || !strings.Contains(string(out), "actions: 1") {
			t.Errorf("rex config check after the upgrade: %v\n%s", err, out)
		}
	}
}

// read returns the content of the file at path.
func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// write writes content to the file at path.
func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
