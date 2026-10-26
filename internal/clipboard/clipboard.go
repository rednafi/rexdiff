// Package clipboard writes text to the system clipboard through the first
// clipboard tool on PATH, as herdr-reviewr does. The caller gives the
// environment of the tool, with its PATH.
package clipboard

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/rednafi/rexdiff/internal/proc"
)

// ErrNoTool tells that PATH has no clipboard tool. The user can fix it.
var ErrNoTool = errors.New("no clipboard tool is installed (pbcopy, wl-clipboard, xclip, or xsel)")

// tools are the clipboard tools that read stdin. The first on PATH wins.
var tools = [][]string{
	{"pbcopy"},
	{"wl-copy"},
	{"xclip", "-selection", "clipboard"},
	{"xsel", "--clipboard", "--input"},
}

// Write pipes text into the first clipboard tool on the PATH of env. The
// tool runs with the environment env. If PATH has no clipboard tool, Write
// returns [ErrNoTool]. Write fails if the tool exits with a status that is
// not zero. If ctx is done, the error has the text of [context.Cause].
func Write(ctx context.Context, env []string, text string) error {
	for _, t := range tools {
		if _, err := proc.LookPath(env, t[0]); err != nil {
			continue
		}
		cmd := proc.Command(ctx, env, t[0], t[1:]...)
		cmd.Stdin = strings.NewReader(text)
		// The output goes to the null device. xclip and wl-copy leave a
		// process that keeps the clipboard, and a pipe stays open while it
		// runs. Thus the error has no stderr.
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%s: %w", t[0], proc.Error(ctx, t[0], err, nil))
		}
		return nil
	}
	return ErrNoTool
}
