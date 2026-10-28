//go:build ignore

// Icon generator. Everything magpie shows is drawn from one bird: a round
// little magpie in profile, tail cocked, black and white, on a 44-unit grid.
// magpie.svg is the full drawing, its belly, shoulder and the slits in its
// tail cut out of the black; the app tile uses it from 64px up.
// magpie-small.svg drops the eye, which would only be a speck; the tray and
// the smaller tiles use it, and so does the header in
// internal/gui/assets/index.html.
//
//	go run build/icon/gen.go tray internal/gui/tray.png     # 44px black template icon (macOS menu bar)
//	go run build/icon/gen.go app 64 internal/gui/icon.png   # app icon at a given size
//	make icons                                              # regenerates all of them plus magpie.icns
package main

import (
	_ "embed"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
)

var (
	//go:embed magpie.svg
	fullSVG string
	//go:embed magpie-small.svg
	smallSVG string
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

// tray draws the silhouette as a macOS template image (black + alpha).
func tray() image.Image {
	const S = 44
	cov := coverage(parse(smallSVG), S, 8, 1, 0, 0)
	img := image.NewNRGBA(image.Rect(0, 0, S, S))
	for i, a := range cov {
		img.SetNRGBA(i%S, i/S, color.NRGBA{0, 0, 0, uint8(a*255 + 0.5)})
	}
	return img
}

// app draws a rounded square, white shading to the palest grey, with the
// bird on it in ink.
func app(n int) image.Image {
	S := float64(n)
	// macOS icon grid: the squircle occupies ~80% of the canvas
	inset := S * 0.1
	side := S - 2*inset
	radius := side * 0.225
	k := side * 0.8 / 44
	o := inset + side/2 - 22*k
	ss := 4
	if n <= 64 {
		ss = 8
	}
	// under 64px the eye is a grey speck; leave it out
	shape := fullSVG
	if n < 64 {
		shape = smallSVG
	}
	bird := coverage(parse(shape), n, ss, k, o, o)
	bg1, bg2 := 0xff, 0xec // top, bottom
	const ink = 0x16
	img := image.NewNRGBA(image.Rect(0, 0, n, n))
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			var tile float64
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					fx := float64(x) + (float64(sx)+0.5)/float64(ss)
					fy := float64(y) + (float64(sy)+0.5)/float64(ss)
					if roundRect(fx, fy, inset, inset, side, side, radius) {
						tile++
					}
				}
			}
			if tile == 0 {
				continue
			}
			t := float64(y) / S
			bg := float64(bg1)*(1-t) + float64(bg2)*t
			a := bird[y*n+x]
			g := uint8(bg*(1-a) + ink*a + 0.5)
			img.SetNRGBA(x, y, color.NRGBA{g, g, g, uint8(tile / float64(ss*ss) * 255)})
		}
	}
	return img
}

// coverage rasterises a shape, scaled by k and moved to (ox, oy), onto an n×n
// grid of pixels with ss×ss samples each, and gives each pixel's coverage in
// 0..1. It fills even-odd, a scanline at a time.
func coverage(rings [][][2]float64, n, ss int, k, ox, oy float64) []float64 {
	cov := make([]float64, n*n)
	per := 1 / float64(ss*ss)
	var xs []float64
	for y := 0; y < n*ss; y++ {
		v := ((float64(y)+0.5)/float64(ss) - oy) / k
		xs = xs[:0]
		for _, r := range rings {
			for i, j := 0, len(r)-1; i < len(r); j, i = i, i+1 {
				a, b := r[j], r[i]
				if (a[1] > v) != (b[1] > v) {
					u := a[0] + (v-a[1])*(b[0]-a[0])/(b[1]-a[1])
					xs = append(xs, (u*k+ox)*float64(ss))
				}
			}
		}
		sort.Float64s(xs)
		row := cov[(y/ss)*n:][:n]
		for i := 0; i+1 < len(xs); i += 2 {
			// sample centres x+0.5 that fall between the two crossings
			lo := int(math.Max(0, math.Ceil(xs[i]-0.5)))
			hi := int(math.Min(float64(n*ss), math.Ceil(xs[i+1]-0.5)))
			for x := lo; x < hi; x++ {
				row[x/ss] += per
			}
		}
	}
	return cov
}

// parse reads the path out of one of the SVGs: absolute M, L, C and Z
// commands, each curve cut into short segments, one polygon per subpath.
func parse(svg string) [][][2]float64 {
	d := svg[strings.Index(svg, ` d="`)+4:]
	d = d[:strings.IndexByte(d, '"')]
	f := strings.Fields(strings.NewReplacer("M", " M ", "L", " L ", "C", " C ", "Z", " Z ").Replace(d))
	num := func(i int) float64 { x, _ := strconv.ParseFloat(f[i], 64); return x }
	var rings [][][2]float64
	var pts [][2]float64
	cmd := ""
	for i := 0; i < len(f); {
		if c := f[i]; c == "M" || c == "L" || c == "C" || c == "Z" {
			cmd = c
			i++
		}
		switch cmd {
		case "M", "L":
			if cmd == "M" && len(pts) > 0 {
				rings, pts = append(rings, pts), nil
			}
			pts = append(pts, [2]float64{num(i), num(i + 1)})
			i += 2
			cmd = "L" // further pairs after M are lines
		case "C":
			p := pts[len(pts)-1]
			c := [8]float64{p[0], p[1], num(i), num(i + 1), num(i + 2), num(i + 3), num(i + 4), num(i + 5)}
			for s := 1; s <= 8; s++ {
				t := float64(s) / 8
				m := 1 - t
				pts = append(pts, [2]float64{
					m*m*m*c[0] + 3*m*m*t*c[2] + 3*m*t*t*c[4] + t*t*t*c[6],
					m*m*m*c[1] + 3*m*m*t*c[3] + 3*m*t*t*c[5] + t*t*t*c[7],
				})
			}
			i += 6
		case "Z":
			if len(pts) > 0 {
				rings, pts = append(rings, pts), nil
			}
		}
	}
	if len(pts) > 0 {
		rings = append(rings, pts)
	}
	return rings
}

// roundRect is a rounded square: true inside it.
func roundRect(px, py, x, y, w, h, r float64) bool {
	if px < x || py < y || px > x+w || py > y+h {
		return false
	}
	cx := math.Max(x+r, math.Min(px, x+w-r))
	cy := math.Max(y+r, math.Min(py, y+h-r))
	return math.Hypot(px-cx, py-cy) <= r
}
