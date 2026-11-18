package snapcompact

import (
	"image"
	"math"

	"golang.org/x/image/font"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

// The Silver outline rasterizer follows fontdue 0.9.4, used by pi-natives.
// See omp/FONTDUE-LICENSE. All scanline accesses below are bounds checked.
type ompPoint struct{ x, y float32 }
type ompLine struct{ from, to ompPoint }

type ompOutline struct {
	vertical, diagonal           []ompLine
	xmin, ymin, xmax, ymax, area float32
}

func (o *ompOutline) line(a, b ompPoint) {
	if a.y == b.y {
		return
	}
	o.area += float32((b.y - a.y) * (b.x + a.x))
	o.xmin, o.xmax = min(o.xmin, a.x, b.x), max(o.xmax, a.x, b.x)
	o.ymin, o.ymax = min(o.ymin, a.y, b.y), max(o.ymax, a.y, b.y)
	if a.x == b.x {
		o.vertical = append(o.vertical, ompLine{a, b})
	} else {
		o.diagonal = append(o.diagonal, ompLine{a, b})
	}
}

func (o *ompOutline) curve(a, b ompPoint, areaLimit float32, point func(float32) ompPoint) {
	var flatten func(ompPoint, float32, ompPoint, float32, int)
	flatten = func(a ompPoint, t1 float32, b ompPoint, t2 float32, depth int) {
		mid := (t1 + t2) * 0.5
		p := point(mid)
		area := float32((p.x-a.x)*(b.y-a.y)) - float32((b.x-a.x)*(p.y-a.y))
		if depth < 32 && ompAbs(area) > areaLimit {
			flatten(p, mid, b, t2, depth+1)
			flatten(a, t1, p, mid, depth+1)
		} else {
			o.line(a, b)
		}
	}
	flatten(a, 0, b, 1, 0)
}

func ompTraceSilver(segments sfnt.Segments, units int) ompOutline {
	o := ompOutline{xmin: math.MaxFloat32, ymin: math.MaxFloat32, xmax: -math.MaxFloat32, ymax: -math.MaxFloat32}
	point := func(p fixed.Point26_6) ompPoint { return ompPoint{float32(p.X) / 64, -float32(p.Y) / 64} }
	var pen, start ompPoint
	areaLimit := 6 * float32(units) / 40
	for _, segment := range segments {
		p := point(segment.Args[0])
		switch segment.Op {
		case sfnt.SegmentOpMoveTo:
			o.line(pen, start)
			start, pen = p, p
		case sfnt.SegmentOpLineTo:
			o.line(pen, p)
			pen = p
		case sfnt.SegmentOpQuadTo:
			end := point(segment.Args[1])
			o.curve(pen, end, areaLimit, func(t float32) ompPoint {
				u := 1 - t
				a, b, c := u*u, 2*u*t, t*t
				return ompPoint{float32(a*pen.x) + float32(b*p.x) + float32(c*end.x), float32(a*pen.y) + float32(b*p.y) + float32(c*end.y)}
			})
			pen = end
		case sfnt.SegmentOpCubeTo:
			q, end := point(segment.Args[1]), point(segment.Args[2])
			o.curve(pen, end, areaLimit, func(t float32) ompPoint {
				u := 1 - t
				a, b, c, d := u*u*u, 3*(u*u)*t, 3*u*(t*t), t*t*t
				return ompPoint{float32(a*pen.x) + float32(b*p.x) + float32(c*q.x) + float32(d*end.x), float32(a*pen.y) + float32(b*p.y) + float32(c*q.y) + float32(d*end.y)}
			})
			pen = end
		}
	}
	o.line(pen, start)
	return o
}

func ompAbs(x float32) float32   { return math.Float32frombits(math.Float32bits(x) & 0x7fffffff) }
func ompFract(x float32) float32 { return x - float32(int(x)) }

func ompPositiveFract(x float32) float32 {
	x = ompFract(x)
	if x < 0 {
		x++
	}
	return x
}

func ompRasterOffset(x float32, nudge bool) int {
	if nudge && x > 0 {
		return int(math.Float32frombits(math.Float32bits(x) - 1))
	}
	return int(x)
}

type ompSilverRaster struct {
	width int
	area  []float32
}

func (r ompSilverRaster) add(i int, height, midpoint float32) {
	if i < 0 || i+1 >= len(r.area) {
		return
	}
	m := height * midpoint
	r.area[i] += height - m
	r.area[i+1] += m
}

func (r ompSilverRaster) line(a, b ompPoint, dx, dy, scale float32) {
	x0, y0, x1, y1 := a.x, a.y, b.x, b.y
	startX, startY := ompRasterOffset(x0, dx < 0), ompRasterOffset(y0, dy < 0)
	endX, endY := ompRasterOffset(x1, dx > 0), ompRasterOffset(y1, dy > 0)
	sy := 1
	targetY := float32(startY + 1)
	if dy < 0 {
		sy, targetY = -1, float32(startY)
	}
	index := startX + startY*r.width
	previousY := y0
	if dx == 0 {
		mid := ompFract(x0)
		for n := int(ompAbs(float32(startY - endY))); n > 0; n-- {
			r.add(index, previousY-targetY, mid)
			index += sy * r.width
			previousY = targetY
			targetY += float32(sy)
		}
		r.add(endX+endY*r.width, previousY-y1, mid)
		return
	}
	sx := 1
	targetX := float32(startX + 1)
	if dx < 0 {
		sx, targetX = -1, float32(startX)
	}
	invScale := 1 / scale
	tdx, tdy := (1/dx)*invScale, (1/dy)*invScale
	tmx, tmy := tdx*(targetX-x0), tdy*(targetY-y0)
	tdx, tdy = ompAbs(tdx), ompAbs(tdy)
	dx, dy = dx*scale, dy*scale
	previousX := x0
	for n := int(ompAbs(float32(startX-endX)) + ompAbs(float32(startY-endY))); n > 0; n-- {
		i := index
		var nextX, nextY float32
		if tmx < tmy {
			nextX, nextY = targetX, float32(tmx*dy)+y0
			tmx += tdx
			targetX += float32(sx)
			index += sx
		} else {
			nextX, nextY = float32(tmy*dx)+x0, targetY
			tmy += tdy
			targetY += float32(sy)
			index += sy * r.width
		}
		r.add(i, previousY-nextY, ompFract((previousX+nextX)*0.5))
		previousX, previousY = nextX, nextY
	}
	r.add(endX+endY*r.width, previousY-y1, ompFract((previousX+x1)*0.5))
}

func (o ompOutline) bitmap(scale float32) (*image.Alpha, int, int) {
	xmin, ymin := o.xmin*scale, o.ymin*scale
	width, height := (o.xmax-o.xmin)*scale, (o.ymax-o.ymin)*scale
	offsetX, offsetY := ompPositiveFract(xmin), ompPositiveFract(1-ompFract(height)-ompFract(ymin))
	w, h := int(math.Ceil(float64(width+offsetX))), int(math.Ceil(float64(height+offsetY)))
	if w <= 0 || h <= 0 || w > 4096 || h > 4096 {
		return nil, 0, 0
	}
	raster := ompSilverRaster{width: w, area: make([]float32, ((w*h+3)/4)*4+4)}
	position := func(p ompPoint) ompPoint {
		return ompPoint{float32((p.x-o.xmin)*scale) + offsetX, float32((o.ymax-p.y)*scale) + offsetY}
	}
	for _, group := range [][]ompLine{o.vertical, o.diagonal} {
		for _, line := range group {
			a, b := line.from, line.to
			if o.area > 0 {
				a, b = b, a
			}
			raster.line(position(a), position(b), b.x-a.x, a.y-b.y, scale)
		}
	}
	mask := image.NewAlpha(image.Rect(0, 0, w, h))
	var offset float32
	// Use the same four-lane accumulation as OMP's x86 fontdue build.
	for i := 0; i < len(mask.Pix); i += 4 {
		v := raster.area[i : i+4]
		prefix := [4]float32{v[0], v[1] + v[0], (v[2] + v[1]) + v[0], (v[3] + v[2]) + (v[1] + v[0])}
		for k := range prefix {
			prefix[k] += offset
			if i+k < len(mask.Pix) {
				mask.Pix[i+k] = byte(min(float32(255), ompAbs(prefix[k]*255.9)))
			}
		}
		offset = prefix[3]
	}
	left := int(math.Floor(float64(xmin)))
	top := -h - int(math.Floor(float64(ymin)))
	return mask, left, top
}

func ompRasterizeSilver(r rune, px int) ompRasterGlyph {
	f := ompSilver()
	var buffer sfnt.Buffer
	index, err := f.GlyphIndex(&buffer, r)
	if err != nil || index == 0 {
		return ompRasterGlyph{}
	}
	units := int(f.UnitsPerEm())
	ppem := fixed.I(units)
	advance, err := f.GlyphAdvance(&buffer, index, ppem, font.HintingNone)
	if err != nil {
		return ompRasterGlyph{}
	}
	segments, err := f.LoadGlyph(&buffer, index, ppem, nil)
	if err != nil {
		return ompRasterGlyph{}
	}
	o := ompTraceSilver(segments, units)
	g := ompRasterGlyph{advance: int(math.Ceil(float64(float32(advance) / 64 * float32(px) / float32(units))))}
	if len(o.vertical)+len(o.diagonal) > 0 {
		g.mask, g.left, g.top = o.bitmap(float32(px) / float32(units))
	}
	return g
}
