// Derived from charmbracelet/crush (FSL-1.1-MIT). See ../DERIVED.md.

package diffview

import (
	"cmp"
	"image/color"

	"charm.land/lipgloss/v2"
)

type lineStyle struct {
	num  lipgloss.Style
	mark lipgloss.Style
	code lipgloss.Style
}

type style struct {
	dividerLine lineStyle
	missingLine lineStyle
	equalLine   lineStyle
	addedLine   lineStyle
	deletedLine lineStyle
}

const (
	ansiRed     = "1"
	ansiGreen   = "2"
	ansiYellow  = "3"
	ansiBlue    = "4"
	ansiMagenta = "5"
	ansiCyan    = "6"
	ansiGrey    = "8" // bright black
)

// The amounts of colour that terminalStyle mixes into the background.
const (
	tintDark     = 0.16 // added and deleted lines on a dark background
	tintLight    = 0.13 // added and deleted lines on a light background
	tintNum      = 1.7  // multiplier for the line numbers of those lines
	shadeDivider = 0.07 // hunk headers, towards the foreground
	shadeMissing = 0.03 // the empty side of a split row, towards the foreground
)

// Tints for when the terminal does not report its green or red.
const (
	fallbackGreen = "#2ea043"
	fallbackRed   = "#f85149"
)

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
	num := func(s lipgloss.Style) lipgloss.Style {
		return s.Padding(0, lineNumPadding).Align(lipgloss.Right)
	}
	changed := func(text string, c color.Color) lineStyle {
		textStyle := plain.Foreground(lipgloss.Color(text))
		return lineStyle{
			num:  num(textStyle.Background(tint(c, amt*tintNum))),
			mark: textStyle.Background(tint(c, amt)),
			code: plain.Background(tint(c, amt)),
		}
	}
	divider := plain.Background(subtle(shadeDivider))
	missing := plain.Background(subtle(shadeMissing))
	return style{
		dividerLine: lineStyle{
			num:  num(divider.Foreground(lipgloss.Color(ansiGrey))),
			code: divider.Foreground(lipgloss.Color(ansiCyan)),
		},
		missingLine: lineStyle{
			num:  num(missing),
			mark: missing,
			code: missing,
		},
		equalLine: lineStyle{
			num:  num(plain.Foreground(lipgloss.Color(ansiGrey))),
			code: plain,
		},
		addedLine:   changed(ansiGreen, green),
		deletedLine: changed(ansiRed, red),
	}
}

func mix(a, b color.Color, t float64) color.Color {
	ar, ag, ab, _ := a.RGBA()
	br, bg, bb, _ := b.RGBA()
	// RGBA has 16 bits for each channel. Dividing by 257 gives 8 bits.
	f := func(x, y uint32) uint8 { return uint8((float64(x)*(1-t) + float64(y)*t) / 257) }
	return color.RGBA{R: f(ar, br), G: f(ag, bg), B: f(ab, bb), A: 255}
}
