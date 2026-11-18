package snapcompact

import (
	"context"
	"fmt"

	"github.com/rivo/uniseg"
)

type archiveFrame struct {
	start, end, height int
	shape              archiveShape
	dense              bool
}

// paginate performs the same cell wrapping as rasterization, without creating
// pixels or PNGs. Wide and combined graphemes are indivisible.
func (g *archiveGrid) paginate(ctx context.Context, text string, start, end int, shape archiveShape, dense bool) ([]archiveFrame, error) {
	columns, rows := shape.edge/shape.width, shape.edge/shape.pitch
	if columns < 1 || rows < 1 {
		return nil, ErrRenderInput
	}
	var frames []archiveFrame
	at, x, y := start, 0, 0
	clusters := uniseg.NewGraphemes(text[start:end])
	for clusters.Next() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		begin, finish := clusters.Positions()
		cells, err := g.cells(clusters.Str(), shape)
		if err != nil {
			return nil, err
		}
		if cells > columns {
			return nil, fmt.Errorf("%w: grapheme wider than image", ErrRenderInput)
		}
		if x+cells > columns {
			x = 0
			y++
		}
		if y == rows {
			frames = append(frames, archiveFrame{at, start + begin, rows * shape.pitch, shape, dense})
			at, x, y = start+begin, 0, 0
		}
		x += cells
		if start+finish == end {
			frames = append(frames, archiveFrame{at, end, min(shape.edge, max(64, (y+1)*shape.pitch)), shape, dense})
		}
	}
	return frames, nil
}

func (g *archiveGrid) plan(ctx context.Context, source archiveSource, high archiveShape, head, tail, maxPages int) ([]archiveFrame, error) {
	hi, err := g.paginate(ctx, source.text, head, tail, high, false)
	if err != nil || len(hi) <= maxPages {
		return hi, err
	}
	edges := min(3, (maxPages-1)/2)
	middleStart, middleEnd := head, tail
	if edges > 0 {
		middleStart, middleEnd = hi[edges-1].end, hi[len(hi)-edges].start
	}
	low := archiveShape{"8on16-bw", 8, 16, high.edge}
	middle, err := g.paginate(ctx, source.text, middleStart, middleEnd, low, true)
	if err != nil {
		return nil, err
	}
	budget := maxPages - 2*edges
	if len(middle) > budget {
		middle = middle[len(middle)-budget:]
	}
	frames := make([]archiveFrame, 0, maxPages)
	frames = append(frames, hi[:edges]...)
	frames = append(frames, middle...)
	frames = append(frames, hi[len(hi)-edges:]...)
	return frames, nil
}

// Evict the oldest dense-center frame first, then the oldest interior HQ
// frame. Both chronological image edges remain pinned whenever two fit.
func archiveVictim(frames []archiveFrame) int {
	for i, frame := range frames {
		if frame.dense {
			return i
		}
	}
	if len(frames) > 2 {
		return 1
	}
	return 0
}

func archiveDrop(frames []archiveFrame, index int) []archiveFrame {
	copy(frames[index:], frames[index+1:])
	return frames[:len(frames)-1]
}
