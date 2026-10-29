package diff

import (
	"image/color"
)

// Theme sets the colours of the output. The lightness of Background
// selects the dark or light diff colours.
type Theme struct {
	// Background and Foreground are the terminal's colours, or nil if the
	// terminal does not report them. If Background is nil, lines are not
	// tinted.
	Background, Foreground color.Color
	// Green and Red are the terminal's ANSI colours 2 and 1. Added and
	// deleted lines are tinted towards them. If they are nil, the diff uses
	// fixed colours.
	Green, Red color.Color
}

// dark reports whether the background is dark. A nil background counts as
// dark.
func (t Theme) dark() bool {
	if t.Background == nil {
		return true
	}
	r, g, b, _ := t.Background.RGBA()
	hi, lo := max(r, g, b), min(r, g, b)
	// HSL lightness, from 0 to 1.
	return float64(hi+lo)/2/0xffff < 0.5
}
