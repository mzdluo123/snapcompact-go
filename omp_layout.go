package snapcompact

import (
	"context"
	"strings"
	"unicode/utf16"
)

func ompPaginate(ctx context.Context, text string, shape OMPShape) ([]string, error) {
	grid := ompGridFor(shape)
	if shape.Doc {
		lines := ompWrap(text, grid.columns(), grid.wide)
		pages := make([]string, 0, ompCeilDiv(len(lines), 2*grid.rows))
		for start := 0; start < len(lines); start += 2 * grid.rows {
			pages = append(pages, strings.Join(lines[start:min(len(lines), start+2*grid.rows)], "\n"))
		}
		return pages, ctx.Err()
	}
	var pages []string
	start, cell := 0, 0
	for i, r := range text {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		w := ompCells(r, grid.wide)
		if w == 0 {
			continue
		}
		at := cell
		if w == 2 && grid.cols >= 2 && at%grid.cols == grid.cols-1 {
			at++
		}
		if cell > 0 && at+w > grid.capacity() {
			pages = append(pages, text[start:i])
			start, at = i, 0
		}
		cell = at + w
	}
	if cell > 0 {
		pages = append(pages, text[start:])
	}
	return pages, nil
}

func ompCellLen(text string, wide bool) int {
	n := 0
	for _, r := range text {
		n += ompCells(r, wide)
	}
	return n
}

func ompSliceCells(text string, limit int, wide bool) string {
	n := 0
	for i, r := range text {
		w := ompCells(r, wide)
		if n > 0 && n+w > limit {
			return text[:i]
		}
		n += w
	}
	return text
}

func ompWrap(text string, width int, wide bool) []string {
	var lines []string
	cur, cells := "", 0
	for token := range strings.FieldsFuncSeq(text, ompWhitespace) {
		word, n := token, ompCellLen(token, wide)
		for n > width {
			if cur != "" {
				lines = append(lines, cur)
				cur, cells = "", 0
			}
			head := ompSliceCells(word, width, wide)
			lines = append(lines, head)
			word = word[len(head):]
			n = ompCellLen(word, wide)
		}
		if cur == "" {
			cur, cells = word, n
		} else if cells+1+n <= width {
			cur += " " + word
			cells += 1 + n
		} else {
			lines = append(lines, cur)
			cur, cells = word, n
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}

type ompPlannedFrame struct {
	text  string
	shape OMPShape
}

type ompLayout struct {
	head, tail, kept string
	frames           []ompPlannedFrame
	truncated        int
}

func ompPlan(ctx context.Context, text string, shape OMPShape, limit int) (ompLayout, error) {
	var out ompLayout
	capacity := ompGridFor(shape).capacity()
	units := utf16.Encode([]rune(text))
	if len(units) <= 2*capacity {
		out.head, out.kept = text, text
		return out, nil
	}
	out.head = string(utf16.Decode(units[:capacity]))
	out.tail = string(utf16.Decode(units[len(units)-capacity:]))
	middle := string(utf16.Decode(units[capacity : len(units)-capacity]))
	pages, err := ompPaginate(ctx, middle, shape)
	if err != nil {
		return out, err
	}
	appendFrames := func(pages []string, shape OMPShape) {
		for _, text := range pages {
			out.frames = append(out.frames, ompPlannedFrame{text, shape})
		}
	}
	if shape.Doc {
		kept := pages
		if len(pages) > limit {
			last := len(pages) - limit + 1
			for _, page := range pages[1:last] {
				out.truncated += ompUTF16Len(page)
			}
			kept = append(append([]string{}, pages[:1]...), pages[last:]...)
		}
		appendFrames(kept, shape)
		out.kept = out.head + strings.ReplaceAll(strings.Join(kept, " "), "\n", " ") + out.tail
		return out, nil
	}
	if len(pages) <= limit {
		appendFrames(pages, shape)
		out.kept = text
		return out, nil
	}
	low := ompShapes["8on16-bw"]
	low.Edge = shape.Edge
	if shape.Font == "silver" || ompGridFor(low).capacity() <= capacity {
		low = shape
	}
	edgeFrames := min(3, (limit-1)/2)
	old, recent := pages[:edgeFrames], pages[len(pages)-edgeFrames:]
	oldText, recentText := strings.Join(old, ""), strings.Join(recent, "")
	core := middle[len(oldText) : len(middle)-len(recentText)]
	dense, err := ompPaginate(ctx, core, low)
	if err != nil {
		return out, err
	}
	if budget := limit - 2*edgeFrames; len(dense) > budget {
		drop := strings.Join(dense[:len(dense)-budget], "")
		out.truncated = ompUTF16Len(drop)
		core = core[len(drop):]
		dense = dense[len(dense)-budget:]
	}
	appendFrames(old, shape)
	appendFrames(dense, low)
	appendFrames(recent, shape)
	out.kept = out.head + oldText + core + recentText + out.tail
	return out, nil
}
