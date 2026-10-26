// Package rex controls the Rex terminal through the rex command. In Rex, a
// session has windows, and a window has blocks. A block is one pane, for
// example a terminal that runs a shell or a coding agent.
//
// An agent sends its name and state to Rex with the OSC 7501 escape code,
// for example "pi" and "working". This is the status of the program. A
// [Client] finds the agents in a session, pastes text into an agent, and
// gets each status change.
//
// The caller gives the environment as a value, as in [exec.Cmd.Env]. The
// package finds rex with it and runs rex with it. It does not read the
// environment of the process.
package rex

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
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

	"github.com/rednafi/rexdiff/internal/proc"
)

// Client runs rex commands in one Rex session.
type Client struct {
	block   string   // the block of this process, or ""
	session string   // the ID of the session
	bin     string   // the path of the rex command, or "" for "rex" in PATH
	env     []string // the environment of rex
}

// New returns a client for the session in the environment env, for
// example the environment of the process from [os.Environ]. Rex sets
// REX_SESSION and REX_BLOCK in each block. New returns nil if env is not
// the environment of a Rex block. The client runs rex with env.
func New(env []string) *Client {
	session := proc.Getenv(env, "REX_SESSION")
	if session == "" {
		return nil
	}
	return &Client{
		block:   proc.Getenv(env, "REX_BLOCK"),
		session: session,
		bin:     binFromEnv(env),
		env:     env,
	}
}

// Block returns the block of this process, or "" if Rex does not tell it.
func (c *Client) Block() string { return c.block }

// binFromEnv returns the path of the rex command in env, or "" for the rex
// in PATH. Rex sets REX_BIN_DIR in its terminals. Other programs, for
// example brew, do not have it. If REX_BIN_DIR is empty and PATH has no
// rex, binFromEnv looks in HOME on macOS. It returns the rex of the Rex
// server app, if it is there.
func binFromEnv(env []string) string {
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
	// The app is in the directory of os.UserConfigDir on macOS.
	dir := filepath.Join(home, "Library", "Application Support")
	bin := filepath.Join(dir, "com.superlogical.rex", "RexServer", "Versions", "Current", "Rex Server.app", "Contents", "MacOS", "rex")
	if _, err := os.Stat(bin); err != nil {
		return ""
	}
	return bin
}

// command returns the command "rex args...".
func (c *Client) command(ctx context.Context, args ...string) *exec.Cmd {
	return proc.Command(ctx, c.env, cmp.Or(c.bin, "rex"), args...)
}

// runRex runs "rex args..." and returns the output. If rex fails, the
// error is the first line of the error output of rex.
func (c *Client) runRex(ctx context.Context, args ...string) ([]byte, error) {
	cmd := c.command(ctx, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, proc.Error(ctx, "rex", err, stderr.Bytes())
	}
	return out, nil
}

// runRexJSON runs "rex args..." and decodes the JSON output into v. An
// error of the decoder names the rex command.
func (c *Client) runRexJSON(ctx context.Context, v any, args ...string) error {
	out, err := c.runRex(ctx, args...)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(out, v); err != nil {
		return fmt.Errorf("rex %s: %w", strings.Join(args[:min(len(args), 2)], " "), err)
	}
	return nil
}

// request sends a request to the API of the session. It runs
// "rex api call -s SESSION METHOD ARGS", with args as JSON. If reply is
// not nil, request decodes the reply of Rex into it.
func (c *Client) request(ctx context.Context, method string, args, reply any) error {
	body, err := json.Marshal(args)
	if err != nil {
		return err
	}
	out, err := c.runRex(ctx, "api", "call", "-s", c.session, method, string(body))
	if err != nil || reply == nil {
		return err
	}
	if err := json.Unmarshal(out, reply); err != nil {
		return fmt.Errorf("rex api call %s: %w", method, err)
	}
	return nil
}

// Status is the status that a program sends to Rex with OSC 7501.
type Status struct {
	App   string // the name of the program, for example "pi" or "claude-code"
	State State
	At    time.Time // when Rex got the status, or zero if Rex does not tell
}

// State tells what a program does. The zero State means that the program
// sends no status.
type State string

// The known states of the OSC 7501 protocol. A State can have other
// values, for example "error".
const (
	StateIdle    State = "idle"    // waits for the next instruction of the user
	StateWorking State = "working" // does a task
	StateDone    State = "done"    // waits for the user to see the done task
	StateBlocked State = "blocked" // waits for an action of the user
)

// statusEntry is a status as Rex sends it. A program can send one status
// for itself and one for each of its sub-agents. The status of the
// program itself has no ID.
type statusEntry struct {
	ID    string `json:"id"`
	State State  `json:"state"`
	App   string `json:"app"`
	// EffectiveApp is the name that Rex shows. It can differ from App.
	EffectiveApp string `json:"effective_app"`
	UpdatedAt    string `json:"updated_at"` // RFC 3339
}

// status returns the entry as a Status, with the name that Rex shows.
func (e statusEntry) status() Status {
	at, _ := time.Parse(time.RFC3339Nano, e.UpdatedAt) // zero if bad
	return Status{
		App:   cmp.Or(e.EffectiveApp, e.App),
		State: e.State,
		At:    at,
	}
}

// Status returns the status of the program in block. If the program sends
// no status, Status returns the zero Status. The statuses of the
// sub-agents of the program do not count, as in [Client.Watch].
func (c *Client) Status(ctx context.Context, block string) (Status, error) {
	var reply struct {
		Entries []statusEntry `json:"records"`
	}
	args := []string{"block", "call", "-s", c.session, "-b", block, "program_status"}
	if err := c.runRexJSON(ctx, &reply, args...); err != nil {
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
	// The foreground process is the program that holds the terminal, for
	// example an agent that runs in a shell. The child is the shell.
	var reply struct {
		Foreground process `json:"foreground"`
		Child      process `json:"child"`
	}
	args := []string{"block", "call", "-s", c.session, "-b", block, "process"}
	if err := c.runRexJSON(ctx, &reply, args...); err != nil {
		return "", err
	}
	cwd := cmp.Or(reply.Foreground.Cwd, reply.Child.Cwd)
	return strings.TrimPrefix(cwd, "file://"), nil
}

// Agent is a block whose program sends a status with an app name.
type Agent struct {
	Block  string // the ID of the block
	Name   string // the label of the block, or else the app name
	Window string // the label of the window of the block, or ""
	Cwd    string // the working directory of the program
	Status Status
}

// terminalCreator is the creator that Rex gives to a terminal block.
// Other blocks, for example a browser, cannot run an agent.
const terminalCreator = "com.superlogical.terminal"

// Agents returns the agents in the session, in the order of the layout.
// It does not return the block of the client. If ctx ends, Agents returns
// the cause of ctx.
func (c *Client) Agents(ctx context.Context) ([]Agent, error) {
	// Get the windows at the same time as the blocks.
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

	// Ask each terminal block for its status at the same time. A block is
	// an agent if its program sends an app name.
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
		// A status call that fails at the deadline does not tell that the
		// block is not an agent.
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

// windows returns the windows of the session. If rex fails, windows returns
// nil.
func (c *Client) windows(ctx context.Context) []window {
	var reply struct {
		Windows []window `json:"windows"`
	}
	if c.runRexJSON(ctx, &reply, "session", "inspect", c.session, "--json") != nil {
		return nil
	}
	return reply.Windows
}

// windowLabel returns the label of the window with the ID id. If no window
// has this ID, it returns "".
func windowLabel(windows []window, id string) string {
	if i := slices.IndexFunc(windows, func(w window) bool { return w.ID == id }); i >= 0 {
		return windows[i].Label
	}
	return ""
}

// Resolve returns the ID of the block that target names. The target is a
// block ID, a block label, or the end of a block ID.
func (c *Client) Resolve(ctx context.Context, target string) (string, error) {
	if strings.HasPrefix(target, "block:") {
		return target, nil
	}
	var reply struct {
		Block struct {
			ID string `json:"block_id"`
		} `json:"block"`
	}
	err := c.runRexJSON(ctx, &reply, "block", "inspect", target, "-s", c.session, "--json")
	if err != nil {
		return "", err
	}
	return reply.Block.ID, nil
}

// Paste pastes text into the program in block, as if the user pasted it.
// Paste does not press Enter, so the program does not submit the text.
func (c *Client) Paste(ctx context.Context, block, text string) error {
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

// Split opens a new terminal block next to the block of the client. The
// new block runs the command argv in the directory cwd.
func (c *Client) Split(ctx context.Context, cwd string, argv ...string) error {
	args := []string{"split", "-s", c.session, "-b", c.block, "--cwd", cwd, "--"}
	_, err := c.runRex(ctx, append(args, argv...)...)
	return err
}

// ConfigPath returns the path of the configuration file of Rex in the
// environment env. This is $REX_CONFIG if it is set. If not, it is
// init.lua in the rex directory of $XDG_CONFIG_HOME, or of $HOME/.config.
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

// ReloadConfig tells Rex to read its configuration file again. It runs
// rex with the environment env. It also works outside a Rex session.
func ReloadConfig(ctx context.Context, env []string) error {
	c := &Client{bin: binFromEnv(env), env: env}
	_, err := c.runRex(ctx, "config", "reload")
	return err
}

// Event is a change to a block in the session, or a reconnect.
type Event struct {
	Block  string // the ID of the block
	Closed bool   // true if the block closed
	// Status is the new status of the program in the block. If the program
	// removes its status, Status is the zero Status.
	Status Status
	// Reconnect is true if Watch starts rex again. The events between the
	// two runs of rex are lost. A Reconnect event has no block.
	Reconnect bool
	// Err is the error of a run of rex that fails, with the first line
	// of its stderr. An event with Err has no block. Watch starts rex
	// again after the event.
	Err error
}

const (
	// reconnectDelay is the time that Watch waits before it starts rex again.
	reconnectDelay = 2 * time.Second
	// maxEventLine is the length of the longest event line that Watch reads.
	maxEventLine = 1 << 20
)

// Watch calls fn for each event in the session, from one goroutine. It
// runs "rex events" to get the events. If rex fails, Watch calls fn with
// an event that has the error. If rex stops, Watch starts it again after
// 2 seconds. Then it calls fn with a Reconnect event before the next
// events. Watch returns the cause of ctx when ctx is done.
func (c *Client) Watch(ctx context.Context, fn func(Event)) error {
	for again := false; ; again = true {
		if err := c.watchOnce(ctx, again, fn); err != nil && ctx.Err() == nil {
			fn(Event{Err: err})
		}
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-time.After(reconnectDelay):
		}
	}
}

// watchOnce runs "rex events" and calls fn for each event until rex stops.
// If again is true, the first event is a Reconnect event. It returns the
// error of rex, with the first line of its stderr.
func (c *Client) watchOnce(ctx context.Context, again bool, fn func(Event)) error {
	cmd := c.command(ctx, "events", "-s", c.session, "--json")
	var stderr bytes.Buffer
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
		// A line is too long. Stop rex, because rex can wait until Watch
		// reads the rest of its output.
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return fmt.Errorf("rex events: %w", err)
	}
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("rex events: %w", proc.Error(ctx, "rex", err, stderr.Bytes()))
	}
	return nil
}

// blockEvent is an event of one block in "rex events --json".
type blockEvent struct {
	Block string     `json:"block_id"`
	Name  string     `json:"name"` // for example "program_status_changed"
	Data  statusData `json:"payload"`
}

// statusData is the data of a status event. Changed has the new status.
// Removed has the IDs of the removed statuses.
type statusData struct {
	ID      string      `json:"id"`
	Changed statusEntry `json:"record"`
	Removed []string    `json:"ids"`
}

// parseEvent decodes one line of "rex events --json". It returns false if
// the line is not an event that Watch reports.
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

// statusEvent returns the change of status in b. It reports only the
// status of the program itself, which has no ID. It ignores the statuses
// of its sub-agents. It returns false if b changes no such status.
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
