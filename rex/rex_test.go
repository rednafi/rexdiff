package rex_test

import (
	"context"
	jsonv1 "encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"
	"unicode/utf8"

	"github.com/rednafi/rexdiff/internal/testgit"
	"github.com/rednafi/rexdiff/rex"
)

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
	c, ok := rex.New(env)
	if !ok || c.Block() != "block:self" {
		t.Fatalf("New(%q) = %+v, want the block block:self", env, c)
	}
	return c, dir
}

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

func TestCommands(t *testing.T) {
	t.Parallel()
	c, dir := fakeRex(t)
	ctx := t.Context()
	if id, err := c.ResolveBlock(ctx, "agent"); err != nil || id != "block:pi" {
		t.Errorf("ResolveBlock(agent) = %q, %v, want block:pi, nil", id, err)
	}
	if id, _ := c.ResolveBlock(ctx, "block:x"); id != "block:x" {
		t.Errorf("ResolveBlock(block:x) = %q, want block:x", id)
	}
	_, err := c.ResolveBlock(ctx, "nosuch")
	if err == nil || err.Error() != "no block matches nosuch" {
		t.Errorf("ResolveBlock(nosuch): err = %v, want %q",
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

	b, err := os.ReadFile(filepath.Join(dir, "calls"))
	if err != nil {
		t.Fatal(err)
	}
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

func blockEvent(block, name, payload string) string {
	return `{"type":"session_event","payload":{"session_id":"S",` +
		`"event":{"type":"block_event","payload":{"block_id":"` + block +
		`","name":"` + name + `","payload":` + payload + `}}}}`
}

func TestEvents(t *testing.T) {
	t.Parallel()
	// synctest fakes the clock, so the test does not wait for rex to restart.
	synctest.Test(t, func(t *testing.T) {
		c, dir := fakeRex(t)
		events := strings.Join([]string{
			blockEvent("block:pi", "program_status_changed", `{"id":"",`+
				`"record":{"state":"working","kind":"","app":"x","effective_app":"pi",`+
				`"updated_at":"2026-10-25T21:11:03.5Z"},`+
				`"reason":"report"}`),
			blockEvent("block:pi", "program_status_changed",
				`{"id":"sub","record":{"state":"idle"}}`),
			`not json`,
			// Removing a sub-agent status is not reported.
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
		ctx, cancel := context.WithCancelCause(t.Context())
		defer cancel(fmt.Errorf("test: done: %w", context.Canceled))
		errAll := errors.New("the test has all the events")
		var got []rex.Event
		err := c.Events(ctx, func(e rex.Event) {
			if got = append(got, e); len(got) == len(want) {
				cancel(errAll)
			}
		})
		if !errors.Is(err, errAll) {
			t.Errorf("Events = %v, want the cause %v", err, errAll)
		}
		if !slices.Equal(got, want) {
			t.Errorf("events = \n%+v\nwant:\n%+v", got, want)
		}
	})
}

func TestNew(t *testing.T) {
	t.Parallel()
	for _, env := range [][]string{nil, {"REX_SESSION=S", "REX_SESSION="}} {
		if c, ok := rex.New(env); c != nil || ok {
			t.Errorf("New(%q) outside Rex = %+v, %t, want nil, false", env, c, ok)
		}
	}
}

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

func TestReloadConfigOutsideRex(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "darwin" {
		t.Skip("the Rex server app is on macOS")
	}
	// Outside Rex, as under brew, REX_BIN_DIR is not set and PATH has no rex.
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
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "config reload\n" {
		t.Errorf("the app rex ran with %q, want config reload", b)
	}
}

func TestErrorKeepsCause(t *testing.T) {
	t.Parallel()
	c, _ := client(t, "echo 'Error: no Rex server' >&2\nexit 3\n")
	err := c.Focus(t.Context(), "block:pi")
	ee, ok := errors.AsType[*exec.ExitError](err)
	if err == nil || err.Error() != "no Rex server" || !ok || ee.ExitCode() != 3 {
		t.Errorf("Focus error = %v, exit error %v, want %q wrapping exit 3",
			err, ee, "no Rex server")
	}
}

// TestPasteLong uses a text that Rex refuses in one message.
func TestPasteLong(t *testing.T) {
	t.Parallel()
	// Each call saves its last argument, the JSON, to a file.
	c, dir := client(t, `d="${0%/*}"
n=$(ls "$d" | grep -c '^call')
for last; do :; done
printf '%s' "$last" > "$d/call$n"
`)
	// Rex escapes <, & and > as 6 bytes each.
	text := strings.Repeat("<&>\n", 12_500) + strings.Repeat("日本é\u2028\x01\"\\\t\n", 5000) + "日本"
	if err := c.Paste(t.Context(), "block:pi", text); err != nil {
		t.Fatal(err)
	}
	var parts []string
	for i := 0; ; i++ {
		b, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("call%d", i)))
		if errors.Is(err, os.ErrNotExist) {
			if i < 2 {
				t.Fatalf("Paste made %d calls, want several", i)
			}
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		var args struct {
			Event struct {
				Type    string
				Payload struct{ Text string }
			}
		}
		if err := jsonv1.Unmarshal(b, &args); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		// Rex encodes the text again, escaping HTML characters.
		encoded, _ := jsonv1.Marshal(args.Event.Payload.Text)
		if n := len(encoded); n > 60_000 {
			t.Errorf("call %d holds %d bytes once encoded, want at most 60,000", i, n)
		}
		parts = append(parts, args.Event.Payload.Text)
	}
	for i, p := range parts[:len(parts)-1] {
		if !utf8.ValidString(p) || !strings.HasSuffix(p, "\n") {
			t.Errorf("part %d ends with %q, want whole characters and a newline", i, p[max(0, len(p)-8):])
		}
	}
	if got := strings.Join(parts, ""); got != text {
		t.Errorf("the pasted parts join to %d bytes that differ from the %d of the text",
			len(got), len(text))
	}
}

func TestPastePartFails(t *testing.T) {
	t.Parallel()
	c, _ := client(t, `d="${0%/*}"
echo >> "$d/calls"
if [ "$(wc -l < "$d/calls")" -ge 2 ]; then echo "Error: block closed" >&2; exit 1; fi
`)
	err := c.Paste(t.Context(), "block:pi", strings.Repeat("x", 200_000))
	if pe, ok := errors.AsType[*rex.PasteError](err); !ok || pe.Pasted != 1 || pe.Parts < 3 {
		t.Fatalf("Paste: err = %v, want a PasteError after 1 part of several", err)
	}
	if !strings.Contains(err.Error(), "block closed") {
		t.Errorf("Paste: err = %q, want rex's message in it", err)
	}
}
