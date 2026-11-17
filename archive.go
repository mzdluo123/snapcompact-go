package snapcompact

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"strings"
)

var errArchiveBytes = errors.New("archive encoded byte budget")

type archivePNGWriter struct {
	ctx   context.Context
	out   *bytes.Buffer
	limit int64
}

func (w archivePNGWriter) Write(data []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	if int64(w.out.Len())+int64(len(data)) > w.limit {
		return 0, errArchiveBytes
	}
	return w.out.Write(data)
}

func (r *archiveRenderer) Compact(ctx context.Context, p Projection, config ArchiveConfig) (result Archive, err error) {
	if err = ctx.Err(); err != nil {
		return result, err
	}
	c, err := archiveConfig(config)
	if err != nil {
		return result, err
	}
	source, err := serializeArchive(ctx, p)
	if err != nil {
		return result, err
	}
	high, billing := archiveModel(c.Model, c.MaxEdge)
	result.SourceRunes = source.runes
	result.Shape = fmt.Sprintf("%s/%dpx; 8on16-bw dense middle; Noto 13px Unicode fallback", high.name, high.edge)
	result.TokenBasis, result.TokenEstimateKnown = billing.basis, billing.known
	// Failed compactions claim no coverage. Missing-glyph diagnostics remain
	// available, and the caller can retain the entire original projection.
	defer func() {
		if err != nil {
			result.TextHead, result.TextTail, result.HistoryText = "", "", ""
			result.TextRanges, result.Omitted, result.Render.Pages = nil, nil, nil
			result.RetainedRunes, result.ImageTokens = 0, 0
			result.Render.Uncovered = source.ranges(0, len(source.text))
		}
	}()
	key := sha256.New()
	_, _ = key.Write([]byte("archive-v1\x00"))
	_, _ = key.Write(r.fingerprint[:])
	_, _ = key.Write([]byte(source.text))
	keyEncoder := json.NewEncoder(key)
	if err = keyEncoder.Encode(c); err != nil {
		return result, err
	}
	for _, segment := range p.Segments {
		if err = keyEncoder.Encode(segment.SourceID); err != nil {
			return result, err
		}
	}
	result.Render.CacheKey = hex.EncodeToString(key.Sum(nil))
	capacity := (high.edge / high.width) * (high.edge / high.pitch)
	byteLimit := -1
	if c.MaxTextBytes > 0 {
		byteLimit = int(c.MaxTextBytes)
	}
	head, tail := archiveEdges(ctx, source.text, capacity, capacity, byteLimit)
	if err = ctx.Err(); err != nil {
		return result, err
	}
	if head == tail {
		result.TextHead, result.HistoryText = source.text, source.text
		result.TextRanges = source.ranges(0, len(source.text))
		result.RetainedRunes = source.runes
		return result, nil
	}
	grid, err := r.grid()
	if err != nil {
		return result, err
	}
	defer grid.face.Close()
	pageBudget := c.MaxPages
	if c.MaxImageTokens > 0 {
		pageBudget = int(min(int64(pageBudget), max(int64(1), c.MaxImageTokens/billing.tokens(high.edge, high.edge))))
	}
	frames, err := grid.plan(ctx, source, high, head, tail, pageBudget)
	if err != nil {
		return result, err
	}
	var tokens int64
	for _, frame := range frames {
		tokens += billing.tokens(frame.shape.edge, frame.height)
	}
	for c.MaxImageTokens > 0 && tokens > c.MaxImageTokens && len(frames) > 0 {
		i := archiveVictim(frames)
		tokens -= billing.tokens(frames[i].shape.edge, frames[i].height)
		frames = archiveDrop(frames, i)
	}
	var pages []Page
	var retained []archiveFrame
	var encoded int64
	for _, frame := range frames {
		if err = ctx.Err(); err != nil {
			return result, err
		}
		text := source.text[frame.start:frame.end]
		if missing := grid.missing(text, source, frame.start); len(missing) > 0 {
			result.Render.Missing = missing
			return result, ErrMissingGlyph
		}
		canvas, drawErr := grid.draw(ctx, text, frame.shape, frame.height)
		if drawErr != nil {
			return result, drawErr
		}
		var out bytes.Buffer
		encoder := png.Encoder{CompressionLevel: png.BestSpeed}
		err = encoder.Encode(archivePNGWriter{ctx, &out, c.MaxImageBytes}, canvas)
		if errors.Is(err, errArchiveBytes) {
			err = nil
			continue
		}
		if err != nil {
			return result, err
		}
		pages = append(pages, Page{PNG: out.Bytes(), Width: frame.shape.edge, Height: frame.height, Ranges: source.ranges(frame.start, frame.end)})
		retained = append(retained, frame)
		encoded += int64(out.Len())
		for encoded > c.MaxImageBytes {
			i := archiveVictim(retained)
			encoded -= int64(len(pages[i].PNG))
			copy(pages[i:], pages[i+1:])
			pages[len(pages)-1] = Page{}
			pages = pages[:len(pages)-1]
			retained = archiveDrop(retained, i)
		}
	}
	result.TextHead, result.TextTail = source.text[:head], source.text[tail:]
	result.TextRanges = append(source.ranges(0, head), source.ranges(tail, len(source.text))...)
	var audit strings.Builder
	audit.Grow(len(source.text))
	audit.WriteString(result.TextHead)
	previous := head
	for i, frame := range retained {
		if previous < frame.start {
			result.Omitted = append(result.Omitted, source.ranges(previous, frame.start)...)
		}
		audit.WriteString(source.text[frame.start:frame.end])
		previous = frame.end
		pages[i].Index = i
		result.ImageTokens += billing.tokens(frame.shape.edge, frame.height)
	}
	if previous < tail {
		result.Omitted = append(result.Omitted, source.ranges(previous, tail)...)
	}
	audit.WriteString(result.TextTail)
	result.HistoryText = audit.String()
	result.RetainedRunes = source.runes
	for _, omission := range result.Omitted {
		result.RetainedRunes -= omission.End - omission.Start
	}
	result.Render.Pages = pages
	if err = ctx.Err(); err != nil {
		return result, err
	}
	return result, nil
}

func (g *archiveGrid) missing(text string, source archiveSource, offset int) []MissingGlyph {
	seen := make(map[MissingGlyph]bool)
	var missing []MissingGlyph
	spanIndex := 0
	for at, ch := range text {
		if ch == '\n' || ch == '\r' || ch == '\t' {
			continue
		}
		if _, ok := g.renderer.bitmap[ch]; ok {
			continue
		}
		idx, err := g.renderer.font.GlyphIndex(&g.buffer, ch)
		if idx != 0 && err == nil {
			continue
		}
		for spanIndex < len(source.spans) && source.spans[spanIndex].end <= offset+at {
			spanIndex++
		}
		id := ""
		if spanIndex < len(source.spans) && source.spans[spanIndex].start <= offset+at {
			id = source.spans[spanIndex].source
		}
		m := MissingGlyph{id, ch}
		if !seen[m] {
			seen[m] = true
			missing = append(missing, m)
		}
	}
	return missing
}
