package diff

import (
	"image/color"
)

// Theme sets the colors. The lightness of Background selects dark or
// light diff colors.
type Theme struct {
	// Background and Foreground are nil if the terminal does not report
	// them.
	Background, Foreground color.Color
	// Green and Red tint changed lines. Nil means fixed colors.
	Green, Red color.Color
}

func (t Theme) dark() bool {
	if t.Background == nil {
		return true
	}
	r, g, b, _ := t.Background.RGBA()
	hi, lo := max(r, g, b), min(r, g, b)
	return float64(hi+lo)/2/0xffff < 0.5
}
