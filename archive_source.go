package snapcompact

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/rivo/uniseg"
)

// Offsets index the single immutable serialized string. Source bodies are never
// normalized here: the caller can recover exactly what the projection retained.
type archiveSpan struct {
	start, end  int
	source      string
	checkpoints []archiveCheckpoint
}
type archiveCheckpoint struct{ bytes, runes int }
type archiveSource struct {
	text  string
	spans []archiveSpan
	runes int
}

func serializeArchive(ctx context.Context, p Projection) (archiveSource, error) {
	var out archiveSource
	if len(p.Segments) > 100000 {
		return out, ErrRenderLimit
	}
	var size int64
	for _, s := range p.Segments {
		size += int64(len(s.Text)) + int64(len(s.SourceID)) + int64(len(s.Role)) + int64(len(s.ToolName)) + int64(len(s.CallID)) + int64(len(s.Status))
		if size > 8<<20 {
			return out, ErrRenderLimit
		}
	}
	ids := make(map[string]bool, len(p.Segments))
	calls := make(map[string]int)
	var b strings.Builder
	b.Grow(int(size))
	role := ""
	for i, s := range p.Segments {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		if s.SourceID == "" || ids[s.SourceID] || !utf8.ValidString(s.Text) || !utf8.ValidString(s.SourceID+s.Role+s.ToolName+s.CallID+s.Status) {
			return out, fmt.Errorf("%w: source identity or UTF-8", ErrRenderInput)
		}
		ids[s.SourceID] = true
		if i > 0 {
			b.WriteByte('\n')
		}
		next := s.Role
		if next == "assistant" {
			next = "ai"
		}
		if next == "tool" || s.ToolName != "" {
			next = "call"
		}
		if next == "" {
			next = "unknown"
		}
		if next != role {
			fmt.Fprintf(&b, "¶%s:", next)
			role = next
		}
		fmt.Fprintf(&b, "[s%d]", i+1)
		if previous, paired := calls[s.CallID]; s.CallID != "" && paired && s.Role == "tool" {
			fmt.Fprintf(&b, "→s%d", previous)
		} else if s.ToolName != "" || s.CallID != "" {
			fmt.Fprintf(&b, " %q(%q)", s.ToolName, s.CallID)
			if s.CallID != "" {
				calls[s.CallID] = i + 1
			}
		}
		if s.Status != "" {
			fmt.Fprintf(&b, " status=%q", s.Status)
		}
		b.WriteByte('\n')
		start := b.Len()
		b.WriteString(s.Text)
		span := archiveSpan{start: start, end: b.Len(), source: s.SourceID}
		count, last := 0, 0
		for at := range s.Text {
			if at-last >= 4096 {
				if err := ctx.Err(); err != nil {
					return archiveSource{}, err
				}
				span.checkpoints = append(span.checkpoints, archiveCheckpoint{at, count})
				last = at
			}
			count++
		}
		out.spans = append(out.spans, span)
		out.runes += count
		if b.Len() > 16<<20 {
			return archiveSource{}, ErrRenderLimit
		}
	}
	out.text = b.String()
	return out, nil
}

func (s archiveSource) ranges(start, end int) []Range {
	var result []Range
	for _, span := range s.spans {
		if span.end <= start {
			continue
		}
		if span.start >= end {
			break
		}
		a, b := max(start, span.start), min(end, span.end)
		if a == b {
			continue
		}
		base, offset := span.start, 0
		for _, checkpoint := range span.checkpoints {
			if span.start+checkpoint.bytes > a {
				break
			}
			base, offset = span.start+checkpoint.bytes, checkpoint.runes
		}
		offset += utf8.RuneCountInString(s.text[base:a])
		result = append(result, Range{span.source, "projection", offset, offset + utf8.RuneCountInString(s.text[a:b])})
	}
	return result
}

// Cuts are grapheme boundaries in the serialized stream. The head/tail byte
// allowance includes every role and source label, not just source body bytes.
func archiveEdges(ctx context.Context, text string, headCapacity, tailCapacity, byteLimit int) (int, int) {
	g := uniseg.NewGraphemes(text)
	head, cells := 0, 0
	for g.Next() {
		_, end := g.Positions()
		if ctx.Err() != nil {
			return 0, len(text)
		}
		width := max(1, g.Width())
		if g.Str() == "\t" {
			width = 4
		}
		if cells+width > headCapacity || (byteLimit >= 0 && end > byteLimit/2) {
			break
		}
		cells += width
		head = end
	}
	// Walk forwards once to find the largest fitting suffix, retaining only
	// the last capacity worth of cluster boundaries (bounded by one page).
	type boundary struct{ offset, cells int }
	queue := make([]boundary, tailCapacity+2)
	first, count, total := 0, 0, 0
	g = uniseg.NewGraphemes(text[head:])
	for g.Next() {
		start, end := g.Positions()
		if ctx.Err() != nil {
			return 0, len(text)
		}
		if count == len(queue) {
			first = (first + 1) % len(queue)
			count--
		}
		queue[(first+count)%len(queue)] = boundary{head + start, total}
		count++
		width := max(1, g.Width())
		if g.Str() == "\t" {
			width = 4
		}
		total += width
		for count > 0 {
			b := queue[first]
			if total-b.cells <= tailCapacity && (byteLimit < 0 || head+end-b.offset <= byteLimit-head) {
				break
			}
			first = (first + 1) % len(queue)
			count--
		}
	}
	tail := len(text)
	if count > 0 {
		tail = queue[first].offset
	}
	return head, tail
}
