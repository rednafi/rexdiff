package tui

import (
	"image/color"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

const (
	paletteRed   = 1
	paletteGreen = 2
)

// requestPalette asks for ANSI red and green with OSC 4. Bubble Tea has no
// command for this. It passes the replies to Update as unknown OSC events.
func requestPalette() tea.Msg {
	return tea.RawMsg{Msg: "\x1b]4;1;?\x07\x1b]4;2;?\x07"}
}

// paletteReply parses an OSC 4 reply such as
// "\x1b]4;1;rgb:ffff/0000/0000\x07".
func paletteReply(s string) (i int, c color.Color, ok bool) {
	s, ok = strings.CutPrefix(s, "\x1b]4;")
	if !ok {
		return 0, nil, false
	}
	s = strings.TrimSuffix(strings.TrimSuffix(s, "\x07"), "\x1b\\")
	idx, spec, ok := strings.Cut(s, ";")
	if !ok {
		return 0, nil, false
	}
	i, err := strconv.Atoi(idx)
	if err != nil {
		return 0, nil, false
	}
	if c = ansi.XParseColor(spec); c == nil {
		return 0, nil, false
	}
	return i, c, true
}

func (m *model) onPalette(reply string) {
	i, c, ok := paletteReply(reply)
	if !ok {
		return
	}
	switch {
	case i == paletteRed && !sameColor(m.red, c):
		m.red = c
	case i == paletteGreen && !sameColor(m.green, c):
		m.green = c
	default:
		return
	}
	m.rerender()
}
