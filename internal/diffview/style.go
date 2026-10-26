// Derived from charmbracelet/crush (FSL-1.1-MIT). See ../DERIVED.md.

package diffview

import (
	"cmp"
	"image/color"

	"charm.land/lipgloss/v2"
)

// lineStyle is the style of the parts of one type of line.
type lineStyle struct {
	lineNumber lipgloss.Style
	symbol     lipgloss.Style
	code       lipgloss.Style
}

// style is the style of each type of line.
type style struct {
	dividerLine lineStyle
	missingLine lineStyle
	equalLine   lineStyle
	insertLine  lineStyle
	deleteLine  lineStyle
}

// The colours of the ANSI palette of the terminal, as Lip Gloss colours.
const (
	ansiRed     = "1"
	ansiGreen   = "2"
	ansiYellow  = "3"
	ansiBlue    = "4"
	ansiMagenta = "5"
	ansiCyan    = "6"
	ansiGrey    = "8" // bright black
)

// The amounts that terminalStyle mixes into the background.
const (
	tintDark     = 0.16 // inserted and deleted lines on a dark background
	tintLight    = 0.13 // inserted and deleted lines on a light background
	tintNumber   = 1.7  // multiplies the tint for the numbers of those lines
	shadeDivider = 0.07 // the hunk headers, toward the foreground
	shadeMissing = 0.03 // the empty side of a split row, toward the foreground
)

// The tint colours when the terminal does not report its green or red.
const (
	fallbackGreen = "#2ea043"
	fallbackRed   = "#f85149"
)

// terminalStyle makes the style of [View.Theme] from the terminal colours.
func terminalStyle(bg, fg, green, red color.Color, dark bool) style {
	none := lipgloss.NoColor{}
	green = cmp.Or(green, lipgloss.Color(fallbackGreen))
	red = cmp.Or(red, lipgloss.Color(fallbackRed))
	amt := tintDark
	if !dark {
		amt = tintLight
	}
	tint := func(c color.Color, t float64) color.Color {
		if bg == nil {
			return none
		}
		return mix(bg, c, t)
	}
	subtle := func(t float64) color.Color {
		if bg == nil || fg == nil {
			return none
		}
		return mix(bg, fg, t)
	}
	plain := lipgloss.NewStyle()
	// number is the style of a line number: right-aligned, with a space on
	// each side.
	number := func(s lipgloss.Style) lipgloss.Style {
		return s.Padding(0, lineNumPadding).Align(lipgloss.Right)
	}
	// changed is the style of an inserted or a deleted line, tinted toward c.
	changed := func(text string, c color.Color) lineStyle {
		fg := plain.Foreground(lipgloss.Color(text))
		return lineStyle{
			lineNumber: number(fg.Background(tint(c, amt*tintNumber))),
			symbol:     fg.Background(tint(c, amt)),
			code:       plain.Background(tint(c, amt)),
		}
	}
	divider := plain.Background(subtle(shadeDivider))
	missing := plain.Background(subtle(shadeMissing))
	return style{
		dividerLine: lineStyle{
			lineNumber: number(divider.Foreground(lipgloss.Color(ansiGrey))),
			code:       divider.Foreground(lipgloss.Color(ansiCyan)),
		},
		missingLine: lineStyle{
			lineNumber: number(missing),
			symbol:     missing,
			code:       missing,
		},
		equalLine: lineStyle{
			lineNumber: number(plain.Foreground(lipgloss.Color(ansiGrey))),
			code:       plain,
		},
		insertLine: changed(ansiGreen, green),
		deleteLine: changed(ansiRed, red),
	}
}

// mix mixes a toward b by the amount t, from 0 to 1.
func mix(a, b color.Color, t float64) color.Color {
	ar, ag, ab, _ := a.RGBA()
	br, bg, bb, _ := b.RGBA()
	// RGBA has 16 bits for each channel. Divide by 257 to get 8 bits.
	f := func(x, y uint32) uint8 { return uint8((float64(x)*(1-t) + float64(y)*t) / 257) }
	return color.RGBA{R: f(ar, br), G: f(ag, bg), B: f(ab, bb), A: 255}
}
