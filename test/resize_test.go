package test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func drawn(s string, w, h int) bool {
	lines := strings.Split(s, "\n")
	if strings.TrimSpace(s) == "" || len(lines) > h {
		return false
	}
	for _, l := range lines {
		if ansi.StringWidth(l) > w {
			return false
		}
	}
	if w < 60 || h < 5 {
		return true
	}
	if strings.HasPrefix(strings.TrimSpace(lines[0]), "╭") {
		return sameLine(s, "╰", "╯")
	}
	return len(lines) == h && strings.Contains(lines[0], "1 Changes") && strings.TrimSpace(lines[h-1]) != ""
}

func TestResize(t *testing.T) {
	sizes := [][2]int{{1, 1}, {10, 3}, {300, 80}, {40, 10}, {160, 40}}
	tests := []struct {
		name  string
		open  []string
		shows string
	}{
		{"help", []string{"?"}, "pick commits"},
		{"search", []string{"/", "l", "o", "n", "g"}, "preview · long.txt"},
		{"comment box", []string{"]", "c", "t", "y", "p", "e", "d"}, "enter save"},
		{"quit prompt", []string{"]", "c", "h", "i", "enter", "q"}, "unsent comment will be lost"},
		{"find bar", []string{"ctrl+f", "r", "e", "t"}, "find ret"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tm := start(t, newRepo(t), opts{})
			tm.keys(tt.open...)
			tm.waitFor(tt.shows)
			for _, size := range sizes {
				w, h := size[0], size[1]
				tm.resize(w, h)
				tm.waitUntil(func(s string) bool { return drawn(s, w, h) }, fmt.Sprintf("draw a whole %dx%d frame", w, h))
			}
			tm.waitFor(tt.shows)
			tm.keys("esc")
			tm.waitGone(tt.shows)
		})
	}
}
