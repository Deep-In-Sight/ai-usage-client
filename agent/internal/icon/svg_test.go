package icon

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"image"
	"image/color"
	"io"
	"math"
	"strconv"
	"strings"
)

type rect struct{ x0, y0, x1, y1 float64 }

// Clawd rasterises an SVG made of one <path> whose d uses only M, V, H and
// Z (absolute) to draw rectangles: the rectangles' union is scaled to fit a
// size×size square, keeping its aspect and centred. A pixel is filled when
// its centre is inside a rectangle; there is no anti-aliasing.
func Clawd(svg []byte, size int) (*image.NRGBA, error) {
	d, fill, err := onePath(svg)
	if err != nil {
		return nil, err
	}
	ink, err := parseHex(fill)
	if err != nil {
		return nil, err
	}
	rects, err := parseRects(d)
	if err != nil {
		return nil, err
	}
	bb := rects[0]
	for _, r := range rects[1:] {
		bb = rect{math.Min(bb.x0, r.x0), math.Min(bb.y0, r.y0), math.Max(bb.x1, r.x1), math.Max(bb.y1, r.y1)}
	}
	w, h := bb.x1-bb.x0, bb.y1-bb.y0
	if w <= 0 || h <= 0 {
		return nil, errors.New("clawd: empty drawing")
	}
	scale := float64(size) / math.Max(w, h)
	offX := (float64(size) - w*scale) / 2
	offY := (float64(size) - h*scale) / 2
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			ux := (float64(x)+0.5-offX)/scale + bb.x0
			uy := (float64(y)+0.5-offY)/scale + bb.y0
			for _, r := range rects {
				if ux >= r.x0 && ux < r.x1 && uy >= r.y0 && uy < r.y1 {
					img.SetNRGBA(x, y, ink)
					break
				}
			}
		}
	}
	return img, nil
}

func onePath(svg []byte) (d, fill string, err error) {
	dec := xml.NewDecoder(bytes.NewReader(svg))
	n := 0
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", "", fmt.Errorf("clawd: %w", err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != "path" {
			continue
		}
		n++
		for _, a := range se.Attr {
			switch a.Name.Local {
			case "d":
				d = a.Value
			case "fill":
				fill = a.Value
			}
		}
	}
	if n != 1 {
		return "", "", fmt.Errorf("clawd: %d <path> elements, want 1", n)
	}
	if d == "" || fill == "" {
		return "", "", errors.New("clawd: the path needs d and fill")
	}
	return d, fill, nil
}

func parseHex(s string) (color.NRGBA, error) {
	h := strings.TrimPrefix(s, "#")
	if len(h) == 3 {
		h = string([]byte{h[0], h[0], h[1], h[1], h[2], h[2]})
	}
	v, err := strconv.ParseUint(h, 16, 32)
	if len(h) != 6 || err != nil {
		return color.NRGBA{}, fmt.Errorf("clawd: fill %q is not #rrggbb", s)
	}
	return color.NRGBA{uint8(v >> 16), uint8(v >> 8), uint8(v), 0xff}, nil
}

// parseRects reads subpaths "M x y" followed by V/H moves and Z; each must
// trace an axis-aligned rectangle.
func parseRects(d string) ([]rect, error) {
	toks, err := tokenize(d)
	if err != nil {
		return nil, err
	}
	var out []rect
	var pts [][2]float64
	var cx, cy float64
	i := 0
	num := func() (float64, error) {
		if i >= len(toks) || toks[i].cmd != 0 {
			return 0, errors.New("clawd: number expected")
		}
		i++
		return toks[i-1].num, nil
	}
	closeSub := func() error {
		if len(pts) == 0 {
			return nil
		}
		r, ok := asRect(pts)
		if !ok {
			return errors.New("clawd: a subpath is not a rectangle")
		}
		out = append(out, r)
		pts = nil
		return nil
	}
	for i < len(toks) {
		t := toks[i]
		if t.cmd == 0 {
			return nil, errors.New("clawd: number without a command")
		}
		i++
		switch t.cmd {
		case 'M':
			if err := closeSub(); err != nil {
				return nil, err
			}
			x, err := num()
			if err != nil {
				return nil, err
			}
			y, err := num()
			if err != nil {
				return nil, err
			}
			cx, cy = x, y
			pts = append(pts, [2]float64{cx, cy})
		case 'V', 'H':
			if len(pts) == 0 {
				return nil, errors.New("clawd: line before M")
			}
			for i < len(toks) && toks[i].cmd == 0 {
				v, _ := num()
				if t.cmd == 'V' {
					cy = v
				} else {
					cx = v
				}
				pts = append(pts, [2]float64{cx, cy})
			}
		case 'Z':
			if err := closeSub(); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("clawd: path command %q is not supported (only M, V, H, Z)", t.cmd)
		}
	}
	if len(pts) != 0 {
		return nil, errors.New("clawd: subpath not closed with Z")
	}
	if len(out) == 0 {
		return nil, errors.New("clawd: no rectangles")
	}
	return out, nil
}

func asRect(pts [][2]float64) (rect, bool) {
	if len(pts) > 1 && pts[len(pts)-1] == pts[0] {
		pts = pts[:len(pts)-1]
	}
	if len(pts) != 4 {
		return rect{}, false
	}
	r := rect{math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)}
	for _, p := range pts {
		r = rect{math.Min(r.x0, p[0]), math.Min(r.y0, p[1]), math.Max(r.x1, p[0]), math.Max(r.y1, p[1])}
	}
	for _, p := range pts {
		if (p[0] != r.x0 && p[0] != r.x1) || (p[1] != r.y0 && p[1] != r.y1) {
			return rect{}, false
		}
	}
	return r, r.x1 > r.x0 && r.y1 > r.y0
}

type token struct {
	cmd byte // 0: a number
	num float64
}

func tokenize(d string) ([]token, error) {
	var out []token
	for i := 0; i < len(d); {
		c := d[i]
		switch {
		case c == ' ' || c == ',' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z':
			if c == 'e' || c == 'E' {
				return nil, errors.New("clawd: bad number")
			}
			out = append(out, token{cmd: c})
			i++
		default:
			j := i
			if d[j] == '-' || d[j] == '+' {
				j++
			}
			dot := false
			for j < len(d) && (d[j] >= '0' && d[j] <= '9' || d[j] == '.' && !dot) {
				dot = dot || d[j] == '.'
				j++
			}
			v, err := strconv.ParseFloat(d[i:j], 64)
			if err != nil || j == i {
				return nil, fmt.Errorf("clawd: bad number at %d", i)
			}
			out = append(out, token{num: v})
			i = j
		}
	}
	return out, nil
}
