package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
)

// A 16×16 picture of a known 8×8 pattern (each cell 2×2 pixels) inside a
// transparent margin comes back as that pattern.
func TestMkartSamplesCentres(t *testing.T) {
	want := []string{
		"##....##",
		"#.#..#.#",
		"#..##..#",
		"########",
		".######.",
		"..#..#..",
		".#....#.",
		"#......#",
	}
	img := image.NewNRGBA(image.Rect(0, 0, 20, 20))
	for row, r := range want {
		for col := range r {
			if r[col] != '#' {
				continue
			}
			for dy := 0; dy < 2; dy++ {
				for dx := 0; dx < 2; dx++ {
					img.SetNRGBA(2+col*2+dx, 2+row*2+dy, color.NRGBA{0, 0, 0, 255})
				}
			}
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := run(&b, &out, 8, 8, 128); err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, l := range want {
		lines = append(lines, "\t\""+l+"\",")
	}
	if got := strings.TrimRight(out.String(), "\n"); got != strings.Join(lines, "\n") {
		t.Fatalf("got\n%s", got)
	}
	if _, err := Grid(image.NewNRGBA(image.Rect(0, 0, 4, 4)), 8, 8, 128); err == nil {
		t.Fatal("an empty picture gave a grid")
	}
}
