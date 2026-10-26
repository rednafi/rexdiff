package rex_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rednafi/rexdiff/internal/testgit"
	"github.com/rednafi/rexdiff/rex"
)

// client writes a rex script with body in a new directory. It returns the
// client of New in the environment of the block block:self in the session
// S, and the directory.
func client(t *testing.T, body string) (*rex.Client, string) {
	t.Helper()
	dir := t.TempDir()
	testgit.Script(t, dir, "rex", body)
	env := []string{
		"PATH=/usr/bin:/bin",
		"REX_SESSION=S",
		"REX_BLOCK=block:self",
		"REX_BIN_DIR=" + dir,
	}
	c := rex.New(env)
	if c == nil || c.Block() != "block:self" {
		t.Fatalf("New(%q) = %+v, want the block block:self", env, c)
	}
	return c, dir
}

// fakeRex returns a client of a rex script that answers with recorded
// output. The script logs each call to the file "calls" in its directory,
// and prints the file "events" for "rex events".
func fakeRex(t *testing.T) (*rex.Client, string) {
	t.Helper()
	script := `printf "%s\n" "$*" >> "${0%/*}/calls"
case "$*" in
"api call -s S session.list_blocks {}")
  echo '{"blocks":[
    {"block_id":"block:self","label":"rexdiff","window_id":"w1",
     "creator_name":"com.superlogical.terminal"},
    {"block_id":"block:pi","label":"agent","window_id":"w1",
     "creator_name":"com.superlogical.terminal"},
    {"block_id":"block:web","label":"","window_id":"w1",
     "creator_name":"com.superlogical.browser"},
    {"block_id":"block:sh","label":"shell","window_id":"w2",
     "creator_name":"com.superlogical.terminal"},
    {"block_id":"block:cc","label":"claude-code","window_id":"w2",
     "creator_name":"com.superlogical.terminal"}]}' ;;
"session inspect S --json")
  echo '{"windows":[
    {"window_id":"w1","label":"main"},
    {"window_id":"w2","label":"side"}]}' ;;
"block call -s S -b block:pi program_status")
  echo '{"records":[
    {"id":"sub","state":"idle","app":"sub"},
    {"state":"blocked","kind":"permission","app":"pi","effective_app":"pi"}]}' ;;
"block call -s S -b block:cc program_status")
  echo '{"records":[{"state":"working","app":"claude-code"}]}' ;;
"block call -s S -b block:sh program_status")
  echo '{"records":[]}' ;;
"block call -s S -b "*" process")
  echo '{"foreground":{"cwd":"file:///repo"}}' ;;
"block inspect agent -s S --json")
  echo '{"block":{"block_id":"block:pi"}}' ;;
"block inspect nosuch -s S --json")
  echo 'Error: no block matches nosuch
more detail' >&2; exit 1 ;;
"events -s S --json")
  cat "${0%/*}/events" ;;
*) ;;
esac
`
	return client(t, script)
}

// TestAgents checks that Agents returns only the terminal blocks that send
// an app name, without the block of the client.
func TestAgents(t *testing.T) {
	t.Parallel()
	c, _ := fakeRex(t)
	agents, err := c.Agents(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want := []rex.Agent{
		{
			Block:  "block:pi",
			Name:   "agent",
			Window: "main",
			Cwd:    "/repo",
			Status: rex.Status{App: "pi", State: rex.StateBlocked},
		},
		{
			Block:  "block:cc",
			Name:   "claude-code",
			Window: "side",
			Cwd:    "/repo",
			Status: rex.Status{App: "claude-code", State: rex.StateWorking},
		},
	}
	if !slices.Equal(agents, want) {
		t.Errorf("Agents = \n%+v\nwant:\n%+v", agents, want)
	}
}

// TestCommands checks the rex calls of Resolve, Paste, Focus, Split, and
// SetLabel.
func TestCommands(t *testing.T) {
	t.Parallel()
	c, dir := fakeRex(t)
	ctx := t.Context()
	if id, err := c.Resolve(ctx, "agent"); err != nil || id != "block:pi" {
		t.Errorf("Resolve(agent) = %q, %v, want block:pi, nil", id, err)
	}
	if id, _ := c.Resolve(ctx, "block:x"); id != "block:x" {
		t.Errorf("Resolve(block:x) = %q, want block:x", id)
	}
	_, err := c.Resolve(ctx, "nosuch")
	if err == nil || err.Error() != "no block matches nosuch" {
		t.Errorf("Resolve(nosuch): err = %v, want %q",
			err, "no block matches nosuch")
	}
	if err := c.Paste(ctx, "block:pi", "a\n\tb"); err != nil {
		t.Fatal(err)
	}
	if err := c.Focus(ctx, "block:pi"); err != nil {
		t.Fatal(err)
	}
	if err := c.Split(ctx, "/repo", "sh", "-c", "vi x"); err != nil {
		t.Fatal(err)
	}
	if err := c.SetLabel(ctx, "block:self", "rex\"diff"); err != nil {
		t.Fatal(err)
	}

	b, _ := os.ReadFile(filepath.Join(dir, "calls"))
	calls := string(b)
	for _, want := range []string{
		`api call -s S block.event {"block_id":"block:pi",` +
			`"event":{"type":"paste","payload":{"text":"a\n\tb"}}}`,
		`api call -s S session.focus_block {"block_id":"block:pi"}`,
		`split -s S -b block:self --cwd /repo -- sh -c vi x`,
		`api call -s S session.set_block_label {"block_id":"block:self","label":"rex\"diff"}`,
	} {
		if !strings.Contains(calls, want) {
			t.Errorf("calls = \n%s\nwant a call:\n%s", calls, want)
		}
	}
}

// blockEvent returns a line of "rex events --json" for an event of block
// with the name and the payload.
func blockEvent(block, name, payload string) string {
	return `{"type":"session_event","payload":{"session_id":"S",` +
		`"event":{"type":"block_event","payload":{"block_id":"` + block +
		`","name":"` + name + `","payload":` + payload + `}}}}`
}

// TestWatch checks that Watch reports only the changes of the status of
// the program, with the time of Rex, and closed blocks. The fake rex stops
// after the events, so Watch reports a reconnect and then the events
// again.
func TestWatch(t *testing.T) {
	t.Parallel()
	c, dir := fakeRex(t)
	events := strings.Join([]string{
		blockEvent("block:pi", "program_status_changed", `{"id":"",`+
			`"record":{"state":"working","kind":"","app":"x","effective_app":"pi",`+
			`"updated_at":"2026-10-25T21:11:03.5Z"},`+
			`"reason":"report"}`),
		blockEvent("block:pi", "program_status_changed",
			`{"id":"sub","record":{"state":"idle"}}`),
		`not json`,
		// The removal of a sub record is not a change of the agent.
		blockEvent("block:pi", "program_status_removed",
			`{"ids":["sub"],"reason":"clear"}`),
		blockEvent("block:pi", "program_status_removed",
			`{"ids":[""],"reason":"clear"}`),
		`{"type":"session_event","payload":{"event":{"type":"block_closed",` +
			`"payload":{"block_id":"block:pi"}}}}`,
	}, "\n")
	testgit.WriteFile(t, dir, "events", events+"\n")

	at := time.Date(2026, 10, 25, 21, 11, 3, 5e8, time.UTC)
	once := []rex.Event{
		{Block: "block:pi", Status: rex.Status{App: "pi", State: rex.StateWorking, At: at}},
		{Block: "block:pi"},
		{Block: "block:pi", Closed: true},
	}
	want := slices.Concat(once, []rex.Event{{Reconnect: true}}, once)
	// Stop Watch when it reports all the events.
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	var got []rex.Event
	err := c.Watch(ctx, func(e rex.Event) {
		if got = append(got, e); len(got) == len(want) {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Watch = %v, want context.Canceled", err)
	}
	if !slices.Equal(got, want) {
		t.Errorf("events = \n%+v\nwant:\n%+v", got, want)
	}
}

// TestNew checks that New returns nil outside Rex. The client of the
// other tests comes from New in a fake block.
func TestNew(t *testing.T) {
	t.Parallel()
	for _, env := range [][]string{nil, {"REX_SESSION=S", "REX_SESSION="}} {
		if c := rex.New(env); c != nil {
			t.Errorf("New(%q) outside Rex = %+v, want nil", env, c)
		}
	}
}

// TestConfigPath checks the places of the configuration file of Rex.
func TestConfigPath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		env  []string
		want string
	}{
		{[]string{"HOME=/h", "XDG_CONFIG_HOME=/x", "REX_CONFIG=/r.lua"}, "/r.lua"},
		{[]string{"HOME=/h", "XDG_CONFIG_HOME=/x"}, "/x/rex/init.lua"},
		{[]string{"HOME=/h", "XDG_CONFIG_HOME="}, "/h/.config/rex/init.lua"},
	}
	for _, tt := range tests {
		if got := rex.ConfigPath(tt.env); got != tt.want {
			t.Errorf("ConfigPath(%q) = %q, want %q", tt.env, got, tt.want)
		}
	}
}

// TestReloadConfigOutsideRex checks that ReloadConfig runs the rex of the
// Rex server app outside a Rex terminal.
func TestReloadConfigOutsideRex(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "darwin" {
		t.Skip("the Rex server app is on macOS")
	}
	// Outside a Rex terminal, for example under brew, REX_BIN_DIR is not set
	// and rex is not in PATH.
	home := t.TempDir()
	env := []string{"HOME=" + home, "REX_BIN_DIR=", "PATH=" + t.TempDir()}
	dir := filepath.Join(home, "Library", "Application Support", "com.superlogical.rex", "RexServer", "Versions", "Current", "Rex Server.app", "Contents", "MacOS")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(home, "log")
	testgit.Script(t, dir, "rex", "echo \"$*\" > '"+log+"'\n")
	if err := rex.ReloadConfig(t.Context(), env); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(log); string(b) != "config reload\n" {
		t.Errorf("the app rex ran with %q, want config reload", b)
	}
}

// TestErrorKeepsCause checks that the error of a failed rex is the message
// of rex. The error also wraps the exit error of the process.
func TestErrorKeepsCause(t *testing.T) {
	t.Parallel()
	c, _ := client(t, "echo 'Error: no Rex server' >&2\nexit 3\n")
	err := c.Focus(t.Context(), "block:pi")
	ee, ok := errors.AsType[*exec.ExitError](err)
	if err == nil || err.Error() != "no Rex server" || !ok || ee.ExitCode() != 3 {
		t.Errorf("Focus error = %v, exit error %v, want %q wrapping exit 3",
			err, ok, "no Rex server")
	}
}
