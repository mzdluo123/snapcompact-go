package snapcompact

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/image/font/sfnt"
)

// These fonts come from the pinned upstream native renderer.
//
//go:embed fonts/5x8.bdf
var omp5x8 string

//go:embed fonts/6x12.bdf
var omp6x12 string

//go:embed fonts/unscii-8.hex
var omp8x8 string

//go:embed fonts/Silver.ttf
var ompSilverData []byte

const ompBlack, ompRepeatBand, ompDim = 7, 8, 9

var ompPalette = [...]color.RGBA{
	{255, 255, 255, 255}, {109, 2, 2, 255}, {109, 53, 2, 255}, {24, 109, 2, 255},
	{2, 109, 109, 255}, {2, 32, 109, 255}, {75, 2, 109, 255}, {0, 0, 0, 255},
	{255, 247, 194, 255}, {128, 128, 128, 255},
}

type ompBitmapFont struct {
	width, height, ascent int
	glyphs                map[rune]ompGlyph
}

type ompGlyph struct {
	width, height, x, y int
	rows                []byte
}

var ompBitmapFonts = sync.OnceValue(func() map[string]*ompBitmapFont {
	return map[string]*ompBitmapFont{
		"5x8": ompReadBDF(omp5x8, 5, 8), "6x12": ompReadBDF(omp6x12, 6, 12),
		"8x13": ompReadBDF(archiveBitmap, 8, 13), "8x8": ompReadHex(),
	}
})

var ompSilver = sync.OnceValue(func() *sfnt.Font {
	f, err := sfnt.Parse(ompSilverData)
	if err != nil {
		panic(err)
	}
	return f
})

func ompNumber(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

func ompReadBDF(text string, w, h int) *ompBitmapFont {
	f := &ompBitmapFont{width: w, height: h, glyphs: make(map[rune]ompGlyph)}
	lines := bufio.NewScanner(strings.NewReader(text))
	var g ompGlyph
	var code int
	for lines.Scan() {
		line := lines.Text()
		switch {
		case strings.HasPrefix(line, "FONT_ASCENT "):
			f.ascent = ompNumber(strings.TrimPrefix(line, "FONT_ASCENT "))
		case strings.HasPrefix(line, "ENCODING "):
			code = ompNumber(strings.TrimPrefix(line, "ENCODING "))
		case strings.HasPrefix(line, "BBX "):
			bb := strings.Fields(strings.TrimPrefix(line, "BBX "))
			g = ompGlyph{width: min(8, ompNumber(bb[0])), height: ompNumber(bb[1]), x: ompNumber(bb[2]), y: ompNumber(bb[3])}
		case line == "BITMAP":
			for lines.Scan() && lines.Text() != "ENDCHAR" {
				n, _ := strconv.ParseUint(strings.TrimSpace(lines.Text()), 16, 8)
				g.rows = append(g.rows, byte(n))
			}
			if code >= 0 {
				f.glyphs[rune(code)] = g
			}
		}
	}
	return f
}

func ompReadHex() *ompBitmapFont {
	f := &ompBitmapFont{width: 8, height: 8, ascent: 7, glyphs: make(map[rune]ompGlyph)}
	for line := range strings.SplitSeq(omp8x8, "\n") {
		key, bits, ok := strings.Cut(line, ":")
		if !ok || len(bits) != 16 {
			continue
		}
		code, err := strconv.ParseUint(key, 16, 32)
		if err != nil {
			continue
		}
		g := ompGlyph{width: 8, height: 8, y: -1, rows: make([]byte, 8)}
		for i := range g.rows {
			n, _ := strconv.ParseUint(bits[2*i:2*i+2], 16, 8)
			g.rows[i] = byte(n)
		}
		f.glyphs[rune(code)] = g
	}
	return f
}

func ompSupports(f *ompBitmapFont, buffer *sfnt.Buffer, r rune) bool {
	if f != nil {
		if _, ok := f.glyphs[r]; ok {
			return true
		}
	}
	index, err := ompSilver().GlyphIndex(buffer, r)
	return err == nil && index != 0
}

func ompWide(r rune) bool {
	return r >= 0x1100 && r <= 0x115f || r >= 0x2e80 && r <= 0x2eff ||
		r >= 0x2f00 && r <= 0x2fdf || r >= 0x3000 && r <= 0x303e ||
		r >= 0x3041 && r <= 0x33ff || r >= 0x3400 && r <= 0x4dbf ||
		r >= 0x4e00 && r <= 0x9fff || r >= 0xa000 && r <= 0xa4cf ||
		r >= 0xac00 && r <= 0xd7a3 || r >= 0xf900 && r <= 0xfaff ||
		r >= 0xfe30 && r <= 0xfe4f || r >= 0xff00 && r <= 0xff60 ||
		r >= 0xffe0 && r <= 0xffe6 || r >= 0x20000 && r <= 0x3fffd && !(r >= 0x2fffe && r <= 0x2ffff)
}

func ompCells(r rune, wide bool) int {
	if r == ompDimOn || r == ompDimOff {
		return 0
	}
	if wide && ompWide(r) {
		return 2
	}
	return 1
}

type ompGrid struct {
	cols, rows, width, height, repeat int
	doc, wide, sentenceInk            bool
}

func ompGridFor(s OMPShape) ompGrid {
	return ompGrid{s.Edge / s.CellWidth, s.Edge / s.CellHeight / s.Repeat, s.CellWidth, s.CellHeight, s.Repeat, s.Doc, s.Font != "silver", s.SentenceInk}
}

func (g ompGrid) columns() int {
	if g.doc {
		return max(0, (g.cols-3)/2)
	}
	return g.cols
}

func (g ompGrid) capacity() int {
	if g.doc {
		return 2 * g.columns() * g.rows
	}
	return g.cols * g.rows
}

type ompCell struct {
	ch            rune
	x, row, units int
	ink           byte
}

func ompLayCells(ctx context.Context, text string, grid ompGrid) ([]ompCell, int, error) {
	cells := make([]ompCell, 0, min(len(text), grid.capacity()))
	columns := grid.columns()
	cursor, line, sentence := 0, 0, 0
	dim := false
	for i, r := range text {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		if !grid.doc && cursor >= grid.capacity() {
			break
		}
		switch r {
		case ompDimOn, ompDimOff:
			dim = r == ompDimOn
			continue
		case '\n':
			if grid.doc {
				line++
				cursor = 0
				if line >= grid.rows*2 {
					return cells, grid.rows, nil
				}
				continue
			}
		}
		ink := byte(ompBlack)
		if dim {
			ink = ompDim
		} else if grid.sentenceInk {
			ink = byte(1 + sentence%6)
		}
		if r == '.' || r == '!' || r == '?' {
			next := text[i+1:]
			if strings.HasPrefix(next, " ") || strings.HasPrefix(next, string(ompNewline)) ||
				(grid.doc && strings.HasPrefix(next, "\n")) {
				sentence++
			}
		}
		units := ompCells(r, grid.wide)
		pos := cursor
		if units == 2 && columns >= 2 && pos%columns == columns-1 {
			pos++
		}
		cursor = pos + units
		if grid.doc {
			if cursor <= columns {
				x := (line/grid.rows*(columns+3) + pos) * grid.width
				cells = append(cells, ompCell{r, x, line % grid.rows, units, ink})
			}
		} else if pos < grid.capacity() {
			cells = append(cells, ompCell{r, pos % columns * grid.width, pos / columns, units, ink})
		}
	}
	used := ompCeilDiv(cursor, max(1, grid.cols))
	if grid.doc {
		used = line + 1
	}
	return cells, min(grid.rows, max(1, used)), nil
}

type ompRasterGlyph struct {
	mask               *image.Alpha
	left, top, advance int
}

type ompRasterFonts map[struct {
	r  rune
	px int
}]ompRasterGlyph

func (f ompRasterFonts) glyph(r rune, px int) ompRasterGlyph {
	key := struct {
		r  rune
		px int
	}{r, px}
	if glyph, ok := f[key]; ok {
		return glyph
	}
	glyph := ompRasterizeSilver(r, px)
	f[key] = glyph
	return glyph
}

func ompFill(pixels []byte, stride, height, x, y, w, h int, ink byte) {
	for row := max(0, y); row < min(height, y+h); row++ {
		for col := max(0, x); col < min(stride, x+w); col++ {
			pixels[row*stride+col] = ink
		}
	}
}

func ompRenderIndexed(ctx context.Context, cells []ompCell, grid ompGrid, f *ompBitmapFont, width, height, used int, fonts ompRasterFonts) ([]byte, error) {
	pixels := make([]byte, width*height)
	for row := range used {
		for copy := 1; copy < grid.repeat; copy++ {
			ompFill(pixels, width, height, 0, (row*grid.repeat+copy)*grid.height, width, grid.height, ompRepeatBand)
		}
	}
	for _, cell := range cells {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for copy := range grid.repeat {
			y := (cell.row*grid.repeat + copy) * grid.height
			if cell.ch == ompNewline {
				ompFill(pixels, width, height, cell.x, y, grid.width, grid.height, ompBlack)
				continue
			}
			if glyph, ok := f.glyphs[cell.ch]; ok {
				left, top := cell.x+glyph.x, y+f.ascent-glyph.height-glyph.y
				for sy, bits := range glyph.rows {
					for sx := range glyph.width {
						if bits&(0x80>>sx) != 0 && left+sx >= 0 && left+sx < width && top+sy >= 0 && top+sy < height {
							pixels[(top+sy)*width+left+sx] = cell.ink
						}
					}
				}
				continue
			}
			glyph := fonts.glyph(cell.ch, min(cell.units*grid.width, grid.height))
			if glyph.mask == nil {
				continue
			}
			left := cell.x + max(0, cell.units*grid.width-glyph.advance)/2 + glyph.left
			top := y + f.ascent + glyph.top
			for sy := range glyph.mask.Bounds().Dy() {
				for sx := range glyph.mask.Bounds().Dx() {
					a := glyph.mask.AlphaAt(sx, sy).A
					ink := cell.ink
					switch {
					case a >= 170:
					case ink == ompBlack && a >= 56:
						ink = ompDim
					case a >= 110:
					default:
						continue
					}
					if x, y := left+sx, top+sy; x >= 0 && x < width && y >= 0 && y < height {
						pixels[y*width+x] = ink
					}
				}
			}
		}
	}
	return pixels, nil
}

func ompRenderSilver(ctx context.Context, cells []ompCell, grid ompGrid, canvas *image.RGBA, used int, fonts ompRasterFonts) error {
	px := min(grid.width, grid.height)
	ascent := int(math.Round(float64(float32(px) * 1200 / 1900)))
	for row := range used {
		for copy := 1; copy < grid.repeat; copy++ {
			y := (row*grid.repeat + copy) * grid.height
			draw.Draw(canvas, image.Rect(0, y, canvas.Bounds().Dx(), y+grid.height), image.NewUniform(ompPalette[ompRepeatBand]), image.Point{}, draw.Src)
		}
	}
	for _, cell := range cells {
		if err := ctx.Err(); err != nil {
			return err
		}
		for copy := range grid.repeat {
			y := (cell.row*grid.repeat + copy) * grid.height
			if cell.ch == ompNewline {
				draw.Draw(canvas, image.Rect(cell.x, y, cell.x+grid.width, y+grid.height), image.NewUniform(ompPalette[ompBlack]), image.Point{}, draw.Src)
				continue
			}
			glyph := fonts.glyph(cell.ch, px)
			if glyph.mask == nil {
				continue
			}
			left := cell.x + max(0, grid.width-glyph.advance)/2 + glyph.left
			top := y + ascent + glyph.top
			ink := ompPalette[cell.ink]
			for sy := range glyph.mask.Bounds().Dy() {
				for sx := range glyph.mask.Bounds().Dx() {
					x, y := left+sx, top+sy
					a := uint16(glyph.mask.AlphaAt(sx, sy).A)
					if a == 0 || !image.Pt(x, y).In(canvas.Bounds()) {
						continue
					}
					i := canvas.PixOffset(x, y)
					for channel, c := range [...]byte{ink.R, ink.G, ink.B} {
						canvas.Pix[i+channel] = byte((uint16(canvas.Pix[i+channel])*(255-a) + uint16(c)*a + 127) / 255)
					}
				}
			}
		}
	}
	return nil
}

func ompPaletted(pixels []byte, w, h int) *image.Paletted {
	var used, remap [len(ompPalette)]byte
	for _, n := range pixels {
		used[n] = 1
	}
	palette := make(color.Palette, 0, len(ompPalette))
	for i, n := range used {
		if n != 0 {
			remap[i] = byte(len(palette))
			palette = append(palette, ompPalette[i])
		}
	}
	for i, n := range pixels {
		pixels[i] = remap[n]
	}
	return &image.Paletted{Pix: pixels, Stride: w, Rect: image.Rect(0, 0, w, h), Palette: palette}
}

// RenderOMPFrame rasterizes the reference shape using embedded bitmap and Silver fonts.
func RenderOMPFrame(ctx context.Context, text string, shape OMPShape) (OMPFrame, error) {
	frame := OMPFrame{Shape: shape, Width: shape.Edge}
	if shape.Edge < 1 || shape.Edge > 4096 || shape.Repeat < 1 || shape.Repeat > 2 ||
		shape.CellWidth < 1 || shape.CellHeight < 1 {
		return frame, ErrRenderInput
	}
	grid := ompGridFor(shape)
	if grid.columns() == 0 || grid.rows == 0 {
		return frame, ErrRenderInput
	}
	bitmap := ompBitmapFonts()[shape.Font]
	if bitmap == nil && shape.Font != "silver" {
		return frame, ErrRenderInput
	}
	cells, used, err := ompLayCells(ctx, text, grid)
	if err != nil {
		return frame, err
	}
	frame.Cols, frame.Rows, frame.Characters = grid.columns(), grid.rows, len(cells)
	if shape.Doc {
		visible := 0
		for _, r := range text {
			if r != '\n' && r != ompDimOn && r != ompDimOff {
				visible++
			}
		}
		frame.Characters = min(visible, grid.capacity())
	}
	contentHeight := used * grid.repeat * grid.height
	frame.Height = max(64, contentHeight)
	fonts := make(ompRasterFonts)
	var canvas image.Image
	switch {
	case bitmap == nil:
		rgb := ompWhiteCanvas(frame.Width, frame.Height)
		if err := ompRenderSilver(ctx, cells, grid, rgb, used, fonts); err != nil {
			return frame, err
		}
		canvas = rgb
	case shape.Stretch && (grid.width != bitmap.width || grid.height != bitmap.height):
		src := grid
		src.width, src.height = bitmap.width, bitmap.height
		for i := range cells {
			cells[i].x = cells[i].x / grid.width * src.width
		}
		sw, sh := src.cols*src.width, used*src.repeat*src.height
		pixels, err := ompRenderIndexed(ctx, cells, src, bitmap, sw, sh, used, fonts)
		if err != nil {
			return frame, err
		}
		canvas, err = ompStretch(ctx, pixels, sw, sh, frame.Width, frame.Height, grid.cols*grid.width, contentHeight)
		if err != nil {
			return frame, err
		}
	default:
		pixels, err := ompRenderIndexed(ctx, cells, grid, bitmap, frame.Width, frame.Height, used, fonts)
		if err != nil {
			return frame, err
		}
		canvas = ompPaletted(pixels, frame.Width, frame.Height)
	}
	var out bytes.Buffer
	encoder := png.Encoder{CompressionLevel: png.BestCompression}
	if err := encoder.Encode(archivePNGWriter{ctx: ctx, out: &out, limit: math.MaxInt64}, canvas); err != nil {
		return frame, err
	}
	frame.PNG = out.Bytes()
	return frame, nil
}

func ompWhiteCanvas(w, h int) *image.RGBA {
	canvas := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(canvas, canvas.Bounds(), image.White, image.Point{}, draw.Src)
	return canvas
}

type ompWeights struct {
	start   int
	weights []float32
}

func ompContributions(src, dst int) []ompWeights {
	scale := float32(src) / float32(dst)
	factor := max(scale, 1)
	support := 3 * factor
	result := make([]ompWeights, dst)
	for i := range dst {
		center := (float32(i) + 0.5) * scale
		begin, end := max(0, int(center-support)), min(src, int(math.Ceil(float64(center+support))))
		row := ompWeights{start: begin, weights: make([]float32, end-begin)}
		var sum float32
		for x := range row.weights {
			t := math.Abs(float64((float32(begin+x) + 0.5 - center) / factor))
			var weight float64
			switch {
			case t < 1e-6:
				weight = 1
			case t < 3:
				q := float32(math.Pi) * float32(t)
				third := q / 3
				weight = float64((float32(math.Sin(float64(q))) / q) * (float32(math.Sin(float64(third))) / third))
			}
			row.weights[x] = float32(weight)
			sum += float32(weight)
		}
		if sum != 0 {
			for x := range row.weights {
				row.weights[x] /= sum
			}
		}
		result[i] = row
	}
	return result
}

func ompFused(a, b, c float32) float32 {
	return float32(math.FMA(float64(a), float64(b), float64(c)))
}

func ompStretch(ctx context.Context, pixels []byte, sw, sh, w, h, dw, dh int) (*image.RGBA, error) {
	canvas := ompWhiteCanvas(w, h)
	horiz, vert := ompContributions(sw, dw), ompContributions(sh, dh)
	tmp := make([][3]float32, dw*sh)
	for y := range sh {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for x, axis := range horiz {
			acc := &tmp[y*dw+x]
			for i, weight := range axis.weights {
				c := ompPalette[pixels[y*sw+axis.start+i]]
				acc[0] = ompFused(float32(c.R), weight, acc[0])
				acc[1] = ompFused(float32(c.G), weight, acc[1])
				acc[2] = ompFused(float32(c.B), weight, acc[2])
			}
		}
	}
	rounded := func(c float32) uint8 { return uint8(min(255, max(0, math.Round(float64(c))))) }
	for y, axis := range vert {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for x := range dw {
			var acc [3]float32
			for i, weight := range axis.weights {
				src := tmp[(axis.start+i)*dw+x]
				for k := range acc {
					acc[k] = ompFused(src[k], weight, acc[k])
				}
			}
			canvas.SetRGBA(x, y, color.RGBA{rounded(acc[0]), rounded(acc[1]), rounded(acc[2]), 255})
		}
	}
	return canvas, nil
}
