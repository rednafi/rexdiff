package diff

import (
	"image/color"
)

// Theme sets the colours. The lightness of Background selects dark or
// light diff colours.
type Theme struct {
	// Background and Foreground are nil if the terminal does not report
	// them.
	Background, Foreground color.Color
	// Green and Red tint changed lines. Nil means fixed colours.
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
