//go:build ignore

// Icon generator. Everything dial shows is drawn from one glyph, "gateway":
// three inputs come in from the left, two of them curving, and meet at a
// hollow node in the middle; one line leaves to the right. Many models,
// one endpoint. The same paths live in internal/gui/assets/index.html, on a
// 44-unit grid.
//
//	go run build/icon/gen.go tray internal/gui/tray.png     # 44px black template icon (macOS menu bar)
//	go run build/icon/gen.go app 64 internal/gui/icon.png   # coloured app icon at a given size
//	make icons                                              # regenerates all of them plus dial.icns
package main

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"strconv"
)

func main() {
	var img image.Image
	switch {
	case len(os.Args) == 3 && os.Args[1] == "tray":
		img = tray()
	case len(os.Args) == 4 && os.Args[1] == "app":
		n, _ := strconv.Atoi(os.Args[2])
		img = app(n)
	default:
		fmt.Fprintln(os.Stderr, "usage: gen.go tray <out.png> | gen.go app <size> <out.png>")
		os.Exit(2)
	}
	f, err := os.Create(os.Args[len(os.Args)-1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// tray draws the glyph as a macOS template image (black + alpha).
func tray() image.Image {
	const S = 44
	img := image.NewNRGBA(image.Rect(0, 0, S, S))
	for y := 0; y < S; y++ {
		for x := 0; x < S; x++ {
			a := 0.0
			// supersample 4x4
			for sy := 0; sy < 4; sy++ {
				for sx := 0; sx < 4; sx++ {
					fx := float64(x) + (float64(sx)+0.5)/4
					fy := float64(y) + (float64(sy)+0.5)/4
					if mark(fx, fy) {
						a++
					}
				}
			}
			img.SetNRGBA(x, y, color.NRGBA{0, 0, 0, uint8(a / 16 * 255)})
		}
	}
	return img
}

// app draws a rounded indigo square with the glyph in white.
func app(n int) image.Image {
	S := float64(n)
	img := image.NewNRGBA(image.Rect(0, 0, n, n))
	// macOS icon grid: the squircle occupies ~80% of the canvas
	inset := S * 0.1
	side := S - 2*inset
	radius := side * 0.225
	// the mark fills 75% of the tile, as in the reference SVG (scale 14 on 824)
	k := side * 0.747 / 44
	ox, oy := inset+(side-44*k)/2, inset+(side-44*k)/2
	bg1 := [3]float64{0x5b, 0x55, 0xf0} // top: lighter indigo
	bg2 := [3]float64{0x3f, 0x37, 0xc9} // bottom: deeper
	ss := 4
	if n <= 64 {
		ss = 8
	}
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			var aBg, aFg float64
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					fx := float64(x) + (float64(sx)+0.5)/float64(ss)
					fy := float64(y) + (float64(sy)+0.5)/float64(ss)
					if roundRect(fx, fy, inset, inset, side, side, radius) {
						aBg++
						if mark((fx-ox)/k, (fy-oy)/k) {
							aFg++
						}
					}
				}
			}
			tot := float64(ss * ss)
			t := float64(y) / S
			r := bg1[0]*(1-t) + bg2[0]*t
			g := bg1[1]*(1-t) + bg2[1]*t
			b := bg1[2]*(1-t) + bg2[2]*t
			fg := aFg / tot
			if aBg > 0 {
				fg /= aBg / tot
			}
			img.SetNRGBA(x, y, color.NRGBA{
				uint8(r*(1-fg) + 255*fg), uint8(g*(1-fg) + 255*fg), uint8(b*(1-fg) + 255*fg),
				uint8(aBg / tot * 255),
			})
		}
	}
	return img
}

// mark is the glyph on its 44-unit grid: a 4.5-wide ring of radius 6.5 about
// (22,22); three inputs from x=5, the middle one straight and the outer two
// curving in from y=11 and y=33; one straight output to x=39. Round caps.
func mark(u, v float64) bool {
	const w = 4.5
	if d := math.Hypot(u-22, v-22); math.Abs(d-6.5) <= w/2 {
		return true
	}
	if segDist(u, v, 5, 22, 15.5, 22) <= w/2 || segDist(u, v, 28.5, 22, 39, 22) <= w/2 {
		return true
	}
	for _, c := range [][8]float64{
		{5, 11, 12, 11, 11, 22, 16, 22},
		{5, 33, 12, 33, 11, 22, 16, 22},
	} {
		if bezDist(u, v, c) <= w/2 {
			return true
		}
	}
	return false
}

// bezDist is the distance from (px,py) to a cubic bezier, flattened into
// short segments; plenty for a stroke this wide.
func bezDist(px, py float64, c [8]float64) float64 {
	const n = 32
	best := math.Inf(1)
	x0, y0 := c[0], c[1]
	for i := 1; i <= n; i++ {
		t := float64(i) / n
		m := 1 - t
		x := m*m*m*c[0] + 3*m*m*t*c[2] + 3*m*t*t*c[4] + t*t*t*c[6]
		y := m*m*m*c[1] + 3*m*m*t*c[3] + 3*m*t*t*c[5] + t*t*t*c[7]
		best = math.Min(best, segDist(px, py, x0, y0, x, y))
		x0, y0 = x, y
	}
	return best
}

func roundRect(px, py, x, y, w, h, r float64) bool {
	if px < x || py < y || px > x+w || py > y+h {
		return false
	}
	qx := math.Max(math.Abs(px-(x+w/2))-(w/2-r), 0)
	qy := math.Max(math.Abs(py-(y+h/2))-(h/2-r), 0)
	return math.Hypot(qx, qy) <= r
}

func segDist(px, py, ax, ay, bx, by float64) float64 {
	vx, vy := bx-ax, by-ay
	wx, wy := px-ax, py-ay
	t := math.Max(0, math.Min(1, (wx*vx+wy*vy)/(vx*vx+vy*vy)))
	return math.Hypot(px-(ax+t*vx), py-(ay+t*vy))
}
