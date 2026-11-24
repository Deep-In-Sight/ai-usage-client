package icon

import (
	"image"
	"image/color"
)

// glyphs is a 3×5 pixel font: one string per row, '#' is ink.
var glyphs = map[rune][5]string{
	'0': {"###", "#.#", "#.#", "#.#", "###"},
	'1': {".#.", "##.", ".#.", ".#.", "###"},
	'2': {"###", "..#", "###", "#..", "###"},
	'3': {"###", "..#", "###", "..#", "###"},
	'4': {"#.#", "#.#", "###", "..#", "..#"},
	'5': {"###", "#..", "###", "..#", "###"},
	'6': {"###", "#..", "###", "#.#", "###"},
	'7': {"###", "..#", "..#", "..#", "..#"},
	'8': {"###", "#.#", "###", "#.#", "###"},
	'9': {"###", "#.#", "###", "..#", "###"},
	'-': {"...", "...", "###", "...", "..."},
}

// drawText draws text centred on img, each font pixel scale×scale, one
// column (×scale) between characters, with a one-pixel Outline around the
// ink.
func drawText(img *image.NRGBA, text string, ink color.NRGBA, scale int) {
	size := img.Bounds().Dx()
	n := len(text)
	w := (n*3 + n - 1) * scale
	h := 5 * scale
	drawTextAt(img, text, ink, scale, (size-w)/2, (size-h)/2)
}

// drawTextAt is drawText with the top left corner of the ink at x0, y0.
func drawTextAt(img *image.NRGBA, text string, ink color.NRGBA, scale, x0, y0 int) {
	inkAt := map[image.Point]bool{}
	for i, ch := range text {
		g := glyphs[ch]
		for row := 0; row < 5; row++ {
			for col := 0; col < 3; col++ {
				if g[row][col] != '#' {
					continue
				}
				for sy := 0; sy < scale; sy++ {
					for sx := 0; sx < scale; sx++ {
						inkAt[image.Pt(x0+(i*4+col)*scale+sx, y0+row*scale+sy)] = true
					}
				}
			}
		}
	}
	b := img.Bounds()
	for pt := range inkAt {
		for dy := -1; dy <= 1; dy++ {
			for dx := -1; dx <= 1; dx++ {
				q := image.Pt(pt.X+dx, pt.Y+dy)
				if !inkAt[q] && q.In(b) {
					img.SetNRGBA(q.X, q.Y, Outline)
				}
			}
		}
	}
	for pt := range inkAt {
		if pt.In(b) {
			img.SetNRGBA(pt.X, pt.Y, ink)
		}
	}
}
