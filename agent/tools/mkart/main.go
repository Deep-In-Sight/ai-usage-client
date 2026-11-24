// mkart turns a picture into the pixel grid the tray icons draw (the Go
// string rows of internal/icon's artwork table): the picture's drawn area
// (its non-transparent bounding box) is divided into cols×rows cells and
// each cell is ink ('#') when the pixel at its centre has an alpha of at
// least threshold, else '.'. Test tooling for making artwork from a
// rasterised file; the file itself is never committed.
//
//	go run ./tools/mkart -cols 8 -rows 8 -threshold 128 logo.png
package main

import (
	"flag"
	"fmt"
	"image"
	_ "image/png"
	"io"
	"os"
	"strings"
)

func main() {
	cols := flag.Int("cols", 8, "columns")
	rows := flag.Int("rows", 8, "rows")
	threshold := flag.Int("threshold", 128, "alpha (0-255) from which a cell centre is ink")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: mkart [-cols N] [-rows N] [-threshold A] <png>")
		os.Exit(2)
	}
	f, err := os.Open(flag.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer f.Close()
	if err := run(f, os.Stdout, *cols, *rows, *threshold); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(r io.Reader, w io.Writer, cols, rows, threshold int) error {
	img, _, err := image.Decode(r)
	if err != nil {
		return err
	}
	g, err := Grid(img, cols, rows, threshold)
	if err != nil {
		return err
	}
	for _, row := range g {
		fmt.Fprintf(w, "\t%q,\n", row)
	}
	return nil
}

// Grid samples img's drawn area at the centres of a cols×rows grid.
func Grid(img image.Image, cols, rows, threshold int) ([]string, error) {
	if cols < 1 || rows < 1 {
		return nil, fmt.Errorf("a grid needs at least one cell")
	}
	b := img.Bounds()
	minX, minY, maxX, maxY := b.Max.X, b.Max.Y, b.Min.X-1, b.Min.Y-1
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if alpha(img, x, y) >= threshold {
				minX, minY, maxX, maxY = min(minX, x), min(minY, y), max(maxX, x), max(maxY, y)
			}
		}
	}
	if maxX < minX {
		return nil, fmt.Errorf("nothing drawn at threshold %d", threshold)
	}
	w, h := float64(maxX-minX+1), float64(maxY-minY+1)
	out := make([]string, rows)
	for row := 0; row < rows; row++ {
		var sb strings.Builder
		for col := 0; col < cols; col++ {
			x := minX + int((float64(col)+0.5)*w/float64(cols))
			y := minY + int((float64(row)+0.5)*h/float64(rows))
			if alpha(img, x, y) >= threshold {
				sb.WriteByte('#')
			} else {
				sb.WriteByte('.')
			}
		}
		out[row] = sb.String()
	}
	return out, nil
}

func alpha(img image.Image, x, y int) int {
	_, _, _, a := img.At(x, y).RGBA()
	return int(a >> 8)
}
