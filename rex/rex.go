// Package rex controls the Rex terminal through the rex command.
//
// A Rex session has windows, and a window has blocks, which are panes
// such as a terminal that runs a coding agent. An agent reports its app
// name and state with the OSC 7501 escape sequence. A [Client] lists the
// agents in a session, pastes text into them and watches their status.
package rex

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/rednafi/rexdiff/internal/proc"
)

// Client runs rex commands in one Rex session.
type Client struct {
	block   string   // the ID of the block that runs this process, or ""
	session string   // the session ID
	bin     string   // the path of the rex binary, or "" to use "rex" from PATH
	env     []string // the environment for rex
}

// New returns a client for the Rex session described by env, such as the
// result of [os.Environ]. It reads REX_SESSION and REX_BLOCK, which Rex
// sets in every block. If REX_SESSION is not set, the process does not run
// in Rex, and New returns nil and false. The client runs rex with env.
func New(env []string) (*Client, bool) {
	session := proc.Getenv(env, "REX_SESSION")
	if session == "" {
		return nil, false
	}
	c := &Client{
		block:   proc.Getenv(env, "REX_BLOCK"),
		session: session,
		bin:     binPath(env),
		env:     env,
	}
	return c, true
}

// Block returns the ID of the block that runs this process, or "" if it is
// not known.
func (c *Client) Block() string { return c.block }

// binPath returns the path of the rex binary to use for env, or "" to use
// rex from PATH. It prefers REX_BIN_DIR, which Rex sets in its terminals.
// If that is not set and PATH has no rex, on macOS it returns the rex
// binary inside the Rex Server app in HOME, if it exists.
func binPath(env []string) string {
	if dir := proc.Getenv(env, "REX_BIN_DIR"); dir != "" {
		return filepath.Join(dir, "rex")
	}
	if _, err := proc.LookPath(env, "rex"); err == nil {
		return ""
	}
	home := proc.Getenv(env, "HOME")
	if runtime.GOOS != "darwin" || home == "" {
		return ""
	}
	// On macOS, this is the directory that os.UserConfigDir returns.
	dir := filepath.Join(home, "Library", "Application Support")
	bin := filepath.Join(dir, "com.superlogical.rex", "RexServer", "Versions", "Current", "Rex Server.app", "Contents", "MacOS", "rex")
	if _, err := os.Stat(bin); err != nil {
		return ""
	}
	return bin
}

// command returns a command that runs rex with args.
func (c *Client) command(ctx context.Context, args ...string) *exec.Cmd {
	return proc.Command(ctx, c.env, cmp.Or(c.bin, "rex"), args...)
}

// run runs rex with args and returns its standard output. If rex fails,
// the error includes the first line of its standard error.
func (c *Client) run(ctx context.Context, args ...string) ([]byte, error) {
	cmd := c.command(ctx, args...)
	var stderr proc.Head
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, proc.Error(ctx, "rex", err, stderr.Bytes())
	}
	return out, nil
}

// runJSON runs rex with args and decodes its JSON output into v. A
// decoding error names the rex subcommand.
func (c *Client) runJSON(ctx context.Context, v any, args ...string) error {
	out, err := c.run(ctx, args...)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(out, v); err != nil {
		return fmt.Errorf("rex %s: %w", strings.Join(args[:min(len(args), 2)], " "), err)
	}
	return nil
}

// request calls the session API method with args encoded as JSON. If
// reply is not nil, request decodes the response into it.
func (c *Client) request(ctx context.Context, method string, args, reply any) error {
	// Rex encodes the request again, with its own escaping, so whether
	// this escapes HTML characters does not change the size Rex measures.
	// Leaving them unescaped keeps the command line short and readable.
	body, err := json.Marshal(args, jsontext.EscapeForHTML(false))
	if err != nil {
		return err
	}
	out, err := c.run(ctx, "api", "call", "-s", c.session, method, string(body))
	if err != nil || reply == nil {
		return err
	}
	if err := json.Unmarshal(out, reply); err != nil {
		return fmt.Errorf("rex api call %s: %w", method, err)
	}
	return nil
}

// Status is the status that a program reports to Rex with OSC 7501.
type Status struct {
	App   string // the program name, for example "pi" or "claude-code"
	State State
	At    time.Time // when Rex received the status, or zero if Rex does not say
}

// State describes what a program is doing. The zero State means that the
// program reports no status.
type State string

// The states that the OSC 7501 protocol defines. A State can also have
// other values, such as "error".
const (
	StateIdle    State = "idle"    // waiting for the user's next instruction
	StateWorking State = "working" // working on a task
	StateDone    State = "done"    // finished a task that the user has not seen yet
	StateBlocked State = "blocked" // waiting for the user to act
)

// statusEntry is a status as Rex returns it. A program can report one
// status for itself, with an empty ID, and one for each of its sub-agents.
type statusEntry struct {
	ID    string `json:"id"`
	State State  `json:"state"`
	App   string `json:"app"`
	// EffectiveApp is the name that Rex displays. It can differ from App.
	EffectiveApp string `json:"effective_app"`
	UpdatedAt    string `json:"updated_at"` // RFC 3339
}

// status converts e to a Status, using the name that Rex displays.
func (e statusEntry) status() Status {
	at, _ := time.Parse(time.RFC3339Nano, e.UpdatedAt) // zero if the time does not parse
	return Status{
		App:   cmp.Or(e.EffectiveApp, e.App),
		State: e.State,
		At:    at,
	}
}

// Status returns the status of the program in block, or the zero Status if
// the program reports none. Like [Client.Events], it ignores the statuses
// of sub-agents.
func (c *Client) Status(ctx context.Context, block string) (Status, error) {
	var reply struct {
		Entries []statusEntry `json:"records"`
	}
	args := []string{"block", "call", "-s", c.session, "-b", block, "program_status"}
	if err := c.runJSON(ctx, &reply, args...); err != nil {
		return Status{}, err
	}
	if i := slices.IndexFunc(reply.Entries, func(e statusEntry) bool { return e.ID == "" }); i >= 0 {
		return reply.Entries[i].status(), nil
	}
	return Status{}, nil
}

// Cwd returns the working directory of the program in block.
func (c *Client) Cwd(ctx context.Context, block string) (string, error) {
	type process struct {
		Cwd string `json:"cwd"` // a path, or a file:// URL
	}
	// Foreground is the process that owns the terminal, for example an
	// agent started from a shell. Child is the shell itself.
	var reply struct {
		Foreground process `json:"foreground"`
		Child      process `json:"child"`
	}
	args := []string{"block", "call", "-s", c.session, "-b", block, "process"}
	if err := c.runJSON(ctx, &reply, args...); err != nil {
		return "", err
	}
	cwd := cmp.Or(reply.Foreground.Cwd, reply.Child.Cwd)
	return strings.TrimPrefix(cwd, "file://"), nil
}

// Agent is a block whose program reports a status with an app name.
type Agent struct {
	Block  string // the block ID
	Name   string // the block label, or the app name if the block has no label
	Window string // the label of the block's window, or ""
	Cwd    string // the working directory of the program
	Status Status
}

// terminalCreator is the creator name of terminal blocks. Only a terminal
// block can run an agent.
const terminalCreator = "com.superlogical.terminal"

// Agents returns the agents in the session in layout order, leaving out
// the client's own block. If ctx ends, Agents returns the cause of ctx.
func (c *Client) Agents(ctx context.Context) ([]Agent, error) {
	// List the windows while the blocks are being listed.
	var windows []window
	var wg sync.WaitGroup
	wg.Go(func() { windows = c.windows(ctx) })

	var reply struct {
		Blocks []struct {
			ID       string `json:"block_id"`
			Label    string `json:"label"`
			WindowID string `json:"window_id"`
			Creator  string `json:"creator_name"`
		} `json:"blocks"`
	}
	if err := c.request(ctx, "session.list_blocks", struct{}{}, &reply); err != nil {
		wg.Wait()
		return nil, err
	}

	// Fetch the status of each terminal block in parallel. A block is an
	// agent if its program reports an app name.
	agents := make([]Agent, len(reply.Blocks))
	for i, b := range reply.Blocks {
		if b.ID == c.block || b.Creator != terminalCreator {
			continue
		}
		wg.Go(func() {
			status, err := c.Status(ctx, b.ID)
			if err != nil || status.App == "" {
				return
			}
			// If Cwd fails, keep the agent with an empty working directory.
			cwd, _ := c.Cwd(ctx, b.ID)
			agents[i] = Agent{
				Block:  b.ID,
				Name:   cmp.Or(b.Label, status.App),
				Cwd:    cwd,
				Status: status,
			}
		})
	}
	wg.Wait()
	if ctx.Err() != nil {
		// A status call that failed because ctx ended does not mean that
		// the block is not an agent.
		return nil, context.Cause(ctx)
	}

	for i, b := range reply.Blocks {
		agents[i].Window = windowLabel(windows, b.WindowID)
	}
	notAgent := func(a Agent) bool { return a.Block == "" }
	return slices.DeleteFunc(agents, notAgent), nil
}

// window is a window of the session.
type window struct {
	ID    string `json:"window_id"`
	Label string `json:"label"`
}

// windows returns the windows of the session, or nil if rex fails.
func (c *Client) windows(ctx context.Context) []window {
	var reply struct {
		Windows []window `json:"windows"`
	}
	if c.runJSON(ctx, &reply, "session", "inspect", c.session, "--json") != nil {
		return nil
	}
	return reply.Windows
}

// windowLabel returns the label of the window with the given id, or "" if
// there is no such window.
func windowLabel(windows []window, id string) string {
	if i := slices.IndexFunc(windows, func(w window) bool { return w.ID == id }); i >= 0 {
		return windows[i].Label
	}
	return ""
}

// ResolveBlock returns the ID of the block that target refers to. target
// can be a block ID, a block label or the end of a block ID.
func (c *Client) ResolveBlock(ctx context.Context, target string) (string, error) {
	if strings.HasPrefix(target, "block:") {
		return target, nil
	}
	var reply struct {
		Block struct {
			ID string `json:"block_id"`
		} `json:"block"`
	}
	err := c.runJSON(ctx, &reply, "block", "inspect", target, "-s", c.session, "--json")
	if err != nil {
		return "", err
	}
	return reply.Block.ID, nil
}

// maxPastePart is the most bytes that the text of one paste event may take
// once Rex encodes it as JSON. Rex refuses a control message larger than
// 65,536 bytes, and the rest of the message takes a few hundred bytes, so
// this leaves a wide margin.
const maxPastePart = 48 << 10

// PasteError is the error from a [Client.Paste] that failed after it had
// pasted some parts of the text.
type PasteError struct {
	Pasted, Parts int   // the number of parts pasted, and of all parts
	Err           error // the error from the part that failed
}

// Error returns the counts of parts and the error of the part that failed.
func (e *PasteError) Error() string {
	return fmt.Sprintf("pasted only %d of %d parts: %v", e.Pasted, e.Parts, e.Err)
}

// Unwrap returns e.Err.
func (e *PasteError) Unwrap() error { return e.Err }

// Paste pastes text into the program in block as if the user had pasted
// it. Paste does not press Enter, so the program does not submit the text.
//
// Rex limits the size of one paste, so Paste splits a long text into parts
// and pastes them in order, which the program sees as several pastes. It
// splits only between characters, after a newline where it can. If a part
// fails after others were pasted, the error is a [*PasteError].
func (c *Client) Paste(ctx context.Context, block, text string) error {
	parts := pasteParts(text, maxPastePart)
	for i, part := range parts {
		if err := c.pastePart(ctx, block, part); err != nil {
			if i == 0 {
				return err
			}
			return &PasteError{Pasted: i, Parts: len(parts), Err: err}
		}
	}
	return nil
}

// pastePart pastes text with one paste event.
func (c *Client) pastePart(ctx context.Context, block, text string) error {
	type paste struct {
		Text string `json:"text"`
	}
	type event struct {
		Type    string `json:"type"`
		Payload paste  `json:"payload"`
	}
	args := struct {
		Block string `json:"block_id"`
		Event event  `json:"event"`
	}{
		Block: block,
		Event: event{Type: "paste", Payload: paste{Text: text}},
	}
	return c.request(ctx, "block.event", args, nil)
}

// pasteParts splits text into parts whose JSON encoding, as Rex measures
// it, takes at most limit bytes. Each part ends between two characters,
// after the last newline that fits if it is in the second half of the
// part. It returns one part for a text that fits, including the empty text.
func pasteParts(text string, limit int) []string {
	var parts []string
	for {
		end, newline, size := 0, 0, 0
		for end < len(text) {
			r, n := utf8.DecodeRuneInString(text[end:])
			size += encodedLen(r, n)
			if size > limit && end > 0 {
				break
			}
			end += n
			if r == '\n' {
				newline = end
			}
		}
		if end == len(text) {
			return append(parts, text)
		}
		if newline > end/2 {
			end = newline
		}
		parts = append(parts, text[:end])
		text = text[end:]
	}
}

// encodedLen returns the most bytes that the rune r, which takes n bytes
// of the text, can take in a JSON string. Rex encodes like Go's
// encoding/json package, which escapes the HTML characters <, > and &, the
// line and paragraph separators and most control characters as \uXXXX,
// and replaces an invalid byte with \ufffd.
func encodedLen(r rune, n int) int {
	switch {
	case r == '"' || r == '\\' || r == '\n' || r == '\r' || r == '\t':
		return 2
	case r < 0x20 || r == '<' || r == '>' || r == '&' ||
		r == '\u2028' || r == '\u2029' || r == utf8.RuneError && n == 1:
		return 6
	}
	return n
}

// Focus moves the keyboard focus to block.
func (c *Client) Focus(ctx context.Context, block string) error {
	args := struct {
		Block string `json:"block_id"`
	}{Block: block}
	return c.request(ctx, "session.focus_block", args, nil)
}

// SetLabel sets the label of block.
func (c *Client) SetLabel(ctx context.Context, block, label string) error {
	args := struct {
		Block string `json:"block_id"`
		Label string `json:"label"`
	}{Block: block, Label: label}
	return c.request(ctx, "session.set_block_label", args, nil)
}

// Split opens a new terminal block next to the client's block and runs
// the command argv in it, in the directory cwd.
func (c *Client) Split(ctx context.Context, cwd string, argv ...string) error {
	args := []string{"split", "-s", c.session, "-b", c.block, "--cwd", cwd, "--"}
	_, err := c.run(ctx, append(args, argv...)...)
	return err
}

// ConfigPath returns the path of the Rex configuration file for env. It is
// $REX_CONFIG if set, or else rex/init.lua in $XDG_CONFIG_HOME or
// $HOME/.config.
func ConfigPath(env []string) string {
	if path := proc.Getenv(env, "REX_CONFIG"); path != "" {
		return path
	}
	dir := proc.Getenv(env, "XDG_CONFIG_HOME")
	if dir == "" {
		dir = filepath.Join(proc.Getenv(env, "HOME"), ".config")
	}
	return filepath.Join(dir, "rex", "init.lua")
}

// ReloadConfig tells Rex to read its configuration file again. It runs rex
// with env and also works outside a Rex session.
func ReloadConfig(ctx context.Context, env []string) error {
	c := &Client{bin: binPath(env), env: env}
	_, err := c.run(ctx, "config", "reload")
	return err
}

// Event is a change to a block in the session, or a notice that
// [Client.Events] has restarted rex.
type Event struct {
	Block  string // the block ID
	Closed bool   // true if the block closed
	// Status is the new status of the program in the block, or the zero
	// Status if the program removed its status.
	Status Status
	// Reconnect is true if Events restarted rex. Events that happened while
	// rex was not running are lost. A Reconnect event has no block.
	Reconnect bool
	// Err is the error from a failed rex run, including the first line of
	// its standard error. An event with Err has no block, and Events
	// restarts rex after it.
	Err error
}

const (
	// reconnectDelay is how long Events waits before it restarts rex.
	reconnectDelay = 2 * time.Second
	// maxEventLine is the longest event line, in bytes, that Events accepts.
	maxEventLine = 1 << 20
)

// Events runs "rex events" and calls fn for each event in the session,
// from a single goroutine. If rex fails, Events calls fn with an event
// that holds the error. When rex stops, Events restarts it after
// 2 seconds and reports a Reconnect event before any new events.
// Events returns the cause of ctx when ctx is done.
func (c *Client) Events(ctx context.Context, fn func(Event)) error {
	for again := false; ; again = true {
		if err := c.eventsOnce(ctx, again, fn); err != nil && ctx.Err() == nil {
			fn(Event{Err: err})
		}
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-time.After(reconnectDelay):
		}
	}
}

// eventsOnce runs "rex events" once and calls fn for each event until rex
// stops. If again is true, it first reports a Reconnect event. The
// returned error includes the first line of rex's standard error.
func (c *Client) eventsOnce(ctx context.Context, again bool, fn func(Event)) error {
	cmd := c.command(ctx, "events", "-s", c.session, "--json")
	var stderr proc.Head
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("rex events: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("rex events: %w", proc.Error(ctx, "rex", err, nil))
	}
	if again {
		fn(Event{Reconnect: true})
	}
	lines := bufio.NewScanner(out)
	lines.Buffer(nil, maxEventLine)
	for lines.Scan() {
		if ev, ok := parseEvent(lines.Bytes()); ok {
			fn(ev)
		}
	}
	if err := lines.Err(); err != nil {
		// A line is too long. Kill rex, because it can block until someone
		// reads its output.
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return fmt.Errorf("rex events: %w", err)
	}
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("rex events: %w", proc.Error(ctx, "rex", err, stderr.Bytes()))
	}
	return nil
}

// blockEvent is an event about one block in the output of
// "rex events --json".
type blockEvent struct {
	Block string     `json:"block_id"`
	Name  string     `json:"name"` // for example "program_status_changed"
	Data  statusData `json:"payload"`
}

// statusData is the payload of a status event. Changed holds the new
// status and Removed holds the IDs of the removed statuses.
type statusData struct {
	ID      string      `json:"id"`
	Changed statusEntry `json:"record"`
	Removed []string    `json:"ids"`
}

// parseEvent decodes one line of "rex events --json" output. It returns
// false if the line is not an event that Events reports.
func parseEvent(line []byte) (Event, bool) {
	var msg struct {
		Payload struct {
			Event struct {
				// Type is "block_event" or "block_closed".
				Type  string     `json:"type"`
				Block blockEvent `json:"payload"`
			} `json:"event"`
		} `json:"payload"`
	}
	if json.Unmarshal(line, &msg) != nil {
		return Event{}, false
	}
	e := msg.Payload.Event
	switch e.Type {
	case "block_closed":
		return Event{Block: e.Block.Block, Closed: true}, true
	case "block_event":
		return statusEvent(e.Block)
	}
	return Event{}, false
}

// statusEvent returns the status change in b. It reports only changes to
// the program's own status, which has an empty ID, and ignores sub-agents.
// It returns false if b does not change that status.
func statusEvent(b blockEvent) (Event, bool) {
	ev := Event{Block: b.Block}
	switch b.Name {
	case "program_status_changed":
		if b.Data.ID != "" {
			return Event{}, false
		}
		ev.Status = b.Data.Changed.status()
		return ev, true
	case "program_status_removed":
		if len(b.Data.Removed) > 0 && !slices.Contains(b.Data.Removed, "") {
			return Event{}, false
		}
		return ev, true
	}
	return Event{}, false
}
