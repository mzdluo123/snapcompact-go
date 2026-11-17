package snapcompact

import (
	"bufio"
	"context"
	"crypto/sha256"
	_ "embed"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"strconv"
	"strings"

	"github.com/rivo/uniseg"
	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

// Public domain X.Org fixed 8x13, distributed with its embedded license.
//
//go:embed fonts/8x13.bdf
var archiveBitmap string

type archiveRenderer struct {
	*pngRenderer
	bitmap      map[rune][13]byte
	fingerprint [32]byte
}

func NewArchiveRenderer() (ArchiveRenderer, error) {
	base, err := NewRenderer()
	if err != nil {
		return nil, err
	}
	r := &archiveRenderer{pngRenderer: base.(*pngRenderer), bitmap: make(map[rune][13]byte)}
	scanner := bufio.NewScanner(strings.NewReader(archiveBitmap))
	var code int64
	var glyph [13]byte
	row := -1
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "ENCODING "):
			code, err = strconv.ParseInt(strings.TrimPrefix(line, "ENCODING "), 10, 32)
			if err != nil {
				return nil, fmt.Errorf("bitmap encoding: %w", err)
			}
		case line == "BITMAP":
			row = 0
			glyph = [13]byte{}
		case line == "ENDCHAR":
			if row != 13 {
				return nil, fmt.Errorf("bitmap glyph %d has %d rows", code, row)
			}
			r.bitmap[rune(code)] = glyph
			row = -1
		case row >= 0:
			if row >= 13 {
				return nil, fmt.Errorf("bitmap glyph overflow")
			}
			bits, e := strconv.ParseUint(line, 16, 8)
			if e != nil {
				return nil, fmt.Errorf("bitmap row: %w", e)
			}
			glyph[row] = byte(bits)
			row++
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	hash := sha256.New()
	_, _ = hash.Write(renderFont)
	_, _ = hash.Write([]byte(archiveBitmap))
	copy(r.fingerprint[:], hash.Sum(nil))
	return r, nil
}

type archiveGrid struct {
	renderer *archiveRenderer
	face     font.Face
	buffer   sfnt.Buffer
}

func (r *archiveRenderer) grid() (*archiveGrid, error) {
	face, err := opentype.NewFace(r.font, &opentype.FaceOptions{Size: 13, DPI: 72, Hinting: font.HintingNone})
	if err != nil {
		return nil, err
	}
	return &archiveGrid{renderer: r, face: face}, nil
}

func (g *archiveGrid) bitmapCluster(text string) (rune, bool) {
	var ch rune
	n := 0
	for _, r := range text {
		ch = r
		n++
	}
	_, exists := g.renderer.bitmap[ch]
	return ch, n == 1 && exists
}

func (g *archiveGrid) cells(text string, shape archiveShape) (int, error) {
	if text == "\n" || text == "\r\n" {
		return 1, nil
	}
	if text == "\t" {
		return 4, nil
	}
	if _, ok := g.bitmapCluster(text); ok {
		return 1, nil
	}
	bounds, advance := font.BoundString(g.face, text)
	width := max(bounds.Max.X.Ceil()-bounds.Min.X.Floor(), advance.Ceil())
	// Missing glyphs are checked only after budget selection: text edges and
	// explicitly omitted frames do not require image-font coverage.
	return max(1, uniseg.StringWidth(text), (width+shape.width-1)/shape.width), nil
}

func (g *archiveGrid) draw(ctx context.Context, text string, shape archiveShape, height int) (*image.Gray, error) {
	canvas := image.NewGray(image.Rect(0, 0, shape.edge, height))
	draw.Draw(canvas, canvas.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	columns := shape.edge / shape.width
	x, y := 0, 0
	clusters := uniseg.NewGraphemes(text)
	black := image.NewUniform(color.Black)
	drawer := font.Drawer{Dst: canvas, Src: black, Face: g.face}
	for clusters.Next() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		cluster := clusters.Str()
		cells, err := g.cells(cluster, shape)
		if err != nil {
			return nil, err
		}
		if x+cells > columns {
			x = 0
			y++
		}
		px, py := x*shape.width, y*shape.pitch
		if py+shape.pitch > height {
			return nil, fmt.Errorf("%w: grid overflow", ErrRenderLimit)
		}
		switch cluster {
		case "\n", "\r\n":
			draw.Draw(canvas, image.Rect(px, py+(shape.pitch-13)/2, px+8, py+(shape.pitch-13)/2+13), black, image.Point{}, draw.Src)
		case "\t": // Four literal blank cells; text audit retains the tab.
		default:
			if ch, ok := g.bitmapCluster(cluster); ok {
				glyph := g.renderer.bitmap[ch]
				for row, bits := range glyph {
					for col := range 8 {
						if bits&(0x80>>col) != 0 {
							canvas.SetGray(px+col, py+(shape.pitch-13)/2+row, color.Gray{Y: 0})
						}
					}
				}
			} else {
				bounds, _ := font.BoundString(g.face, cluster)
				if bounds.Max.Y.Ceil()-bounds.Min.Y.Floor() > shape.pitch {
					return nil, fmt.Errorf("%w: grapheme exceeds grid height", ErrRenderInput)
				}
				drawer.Dot = fixed.P(px-bounds.Min.X.Floor(), py+(shape.pitch-(bounds.Max.Y.Ceil()-bounds.Min.Y.Floor()))/2-bounds.Min.Y.Floor())
				drawer.DrawString(cluster)
			}
		}
		x += cells
	}
	return canvas, nil
}
