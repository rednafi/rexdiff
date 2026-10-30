// Package rex controls the Rex terminal through the rex command. Agents in
// its blocks report their state with OSC 7501.
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
	block   string
	session string
	bin     string // "" means "rex" from PATH
	env     []string
}

// New returns a client from REX_SESSION and REX_BLOCK, or false outside Rex.
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

// Block returns the ID of the block that runs this process, or "".
func (c *Client) Block() string { return c.block }

// binPath returns "" to use rex from PATH. On macOS it can fall back to the
// Rex Server app.
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
	dir := filepath.Join(home, "Library", "Application Support")
	bin := filepath.Join(dir, "com.superlogical.rex", "RexServer", "Versions", "Current", "Rex Server.app", "Contents", "MacOS", "rex")
	if _, err := os.Stat(bin); err != nil {
		return ""
	}
	return bin
}

func (c *Client) command(ctx context.Context, args ...string) *exec.Cmd {
	return proc.Command(ctx, c.env, cmp.Or(c.bin, "rex"), args...)
}

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

func (c *Client) request(ctx context.Context, method string, args, reply any) error {
	// Rex escapes the request again itself, so HTML escaping here gains
	// nothing. Unescaped text keeps the command line short.
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
	App   string // for example "pi" or "claude-code"
	State State
	At    time.Time // when Rex received the status, or zero
}

// State describes what a program is doing. "" means no status.
type State string

// The states of OSC 7501. Other values, such as "error", can occur.
const (
	StateIdle    State = "idle"
	StateWorking State = "working"
	StateDone    State = "done"    // the user has not seen the result yet
	StateBlocked State = "blocked" // waiting for the user to act
)

// statusEntry has an empty ID for the program itself, else a sub-agent ID.
type statusEntry struct {
	ID           string `json:"id"`
	State        State  `json:"state"`
	App          string `json:"app"`
	EffectiveApp string `json:"effective_app"`
	UpdatedAt    string `json:"updated_at"` // RFC 3339
}

func (e statusEntry) status() Status {
	at, _ := time.Parse(time.RFC3339Nano, e.UpdatedAt)
	return Status{
		App:   cmp.Or(e.EffectiveApp, e.App),
		State: e.State,
		At:    at,
	}
}

// Status returns the status of the program in block, without sub-agents.
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
	// Foreground is the job that owns the terminal. Child is the shell.
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
	Block  string
	Name   string // the block label, or else the app name
	Window string // the window label, or ""
	Cwd    string
	Status Status
}

// Only terminal blocks can run an agent.
const terminalCreator = "com.superlogical.terminal"

// Agents returns the agents in layout order, without the client's own
// block. If ctx ends, Agents returns the cause of ctx.
func (c *Client) Agents(ctx context.Context) ([]Agent, error) {
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
		// A status call that failed because ctx ended does not prove that
		// the block is not an agent.
		return nil, context.Cause(ctx)
	}

	for i, b := range reply.Blocks {
		agents[i].Window = windowLabel(windows, b.WindowID)
	}
	notAgent := func(a Agent) bool { return a.Block == "" }
	return slices.DeleteFunc(agents, notAgent), nil
}

type window struct {
	ID    string `json:"window_id"`
	Label string `json:"label"`
}

func (c *Client) windows(ctx context.Context) []window {
	var reply struct {
		Windows []window `json:"windows"`
	}
	if c.runJSON(ctx, &reply, "session", "inspect", c.session, "--json") != nil {
		return nil
	}
	return reply.Windows
}

func windowLabel(windows []window, id string) string {
	if i := slices.IndexFunc(windows, func(w window) bool { return w.ID == id }); i >= 0 {
		return windows[i].Label
	}
	return ""
}

// ResolveBlock returns the ID of a block given by ID, label or ID suffix.
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

// maxPastePart leaves a wide margin under the 65,536-byte limit of one Rex
// control message.
const maxPastePart = 48 << 10

// PasteError reports a [Client.Paste] that failed after some parts.
type PasteError struct {
	Pasted, Parts int
	Err           error
}

// Error returns the part counts and the error.
func (e *PasteError) Error() string {
	return fmt.Sprintf("pasted only %d of %d parts: %v", e.Pasted, e.Parts, e.Err)
}

// Unwrap returns e.Err.
func (e *PasteError) Unwrap() error { return e.Err }

// Paste pastes text into block without Enter. A long text goes as several
// pastes. If a later part fails, the error is a [*PasteError].
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

// pasteParts ends a part after a newline if one is in its second half.
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

// encodedLen returns the maximum JSON size of r. Rex escapes like Go's
// encoding/json: \uXXXX for <, >, &, U+2028, U+2029 and invalid bytes.
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

// Split runs argv in cwd in a new terminal block next to the client's.
func (c *Client) Split(ctx context.Context, cwd string, argv ...string) error {
	args := []string{"split", "-s", c.session, "-b", c.block, "--cwd", cwd, "--"}
	_, err := c.run(ctx, append(args, argv...)...)
	return err
}

// ConfigPath returns $REX_CONFIG, or rex/init.lua in the XDG config dir.
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

// ReloadConfig tells Rex to read its configuration again. It also works
// outside a Rex session.
func ReloadConfig(ctx context.Context, env []string) error {
	c := &Client{bin: binPath(env), env: env}
	_, err := c.run(ctx, "config", "reload")
	return err
}

// Event is a change to a block, or a notice from [Client.Events].
type Event struct {
	Block  string
	Closed bool
	// Status is zero if the program removed its status.
	Status Status
	// Reconnect is true after Events restarts rex. Events in the gap are lost.
	Reconnect bool
	// Err is from a failed rex run. Events restarts rex after it.
	Err error
}

const (
	reconnectDelay = 2 * time.Second
	maxEventLine   = 1 << 20
)

// Events runs "rex events" and calls fn from one goroutine. It restarts rex
// when rex stops. It returns the cause of ctx when ctx is done.
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
		// A line is too long. Kill rex, because it can block until
		// someone reads its output.
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return fmt.Errorf("rex events: %w", err)
	}
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("rex events: %w", proc.Error(ctx, "rex", err, stderr.Bytes()))
	}
	return nil
}

type blockEvent struct {
	Block string     `json:"block_id"`
	Name  string     `json:"name"`
	Data  statusData `json:"payload"`
}

type statusData struct {
	ID      string      `json:"id"`
	Changed statusEntry `json:"record"`
	Removed []string    `json:"ids"`
}

func parseEvent(line []byte) (Event, bool) {
	var msg struct {
		Payload struct {
			Event struct {
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
