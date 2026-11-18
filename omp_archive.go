package snapcompact

import (
	"context"
	"encoding/base64"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// CompactOMP performs the deterministic OMP archive pass. With a history
// allowance it also applies OMP's frame-sizing reserve to that allowance.
func CompactOMP(ctx context.Context, messages []OMPMessage, options OMPOptions) (OMPArchive, error) {
	if err := ctx.Err(); err != nil {
		return OMPArchive{}, err
	}
	if len(messages) > 100000 || options.MaxFrames < 0 || options.MaxFrames > 80 ||
		options.Target.MaxEdge < 0 || options.MaxImageBytes < 0 || options.HistoryTokens < 0 {
		return OMPArchive{}, ErrRenderInput
	}
	size := 0
	for _, msg := range messages {
		for _, block := range msg.Content {
			size += len(block.Text) + len(block.Arguments) + len(block.Intent) + len(block.Name) + len(block.ID)
			if size > 8<<20 {
				return OMPArchive{}, ErrRenderLimit
			}
		}
	}
	serialization := DefaultOMPSerializeConfig()
	if options.Serialization != nil {
		serialization = *options.Serialization
	}
	if math.IsNaN(serialization.HeadRatio) {
		return OMPArchive{}, ErrRenderInput
	}
	serialized := SerializeOMPWithConfig(messages, options.IncludeThinking, serialization)
	previous, truncated := ompPrevious(options)
	hasDigest := previous == "" && options.PreviousSummary != ""
	probe := serialized
	if previous != "" {
		probe = previous + "\n" + probe
	} else if hasDigest {
		probe = options.PreviousSummary + "\n" + probe
	}
	profile := ompResolveProfile(options.Target)
	shape, scan, err := ompShapeForText(probe, options.Target, profile, options.Variant)
	if err != nil {
		return OMPArchive{}, err
	}
	if options.RejectUnsupported && !scan.safe() {
		return OMPArchive{}, ErrMissingGlyph
	}
	grid := ompGridFor(shape)
	if grid.capacity() <= 0 {
		return OMPArchive{}, ErrRenderInput
	}
	text := ompNormalize(serialized, shape.Font).text
	if previous != "" {
		if text != "" {
			text = previous + string(ompNewline) + text
		} else {
			text = previous
		}
	} else if hasDigest {
		summary := options.PreviousSummary
		if !options.IncludeThinking {
			summary = ompStripThinking(summary)
		}
		digest := "[Summary of earlier history] " + ompNormalize(summary, shape.Font).text
		if text != "" {
			text = digest + " [Recent conversation] " + text
		} else {
			text = digest
		}
	}
	text = ompElideData(text, false)
	if len(text) > 16<<20 {
		return OMPArchive{}, ErrRenderLimit
	}
	layout, err := ompPlan(ctx, text, shape, ompMaxFrames(options, shape, profile))
	if err != nil {
		return OMPArchive{}, err
	}
	return ompFinish(ctx, layout, shape, profile.Billing, options, truncated, hasDigest)
}

func ompMaxFrames(options OMPOptions, shape OMPShape, p ompProfile) int {
	limit := options.MaxFrames
	if limit == 0 {
		limit = 80
	}
	if options.HistoryTokens == 0 {
		return limit
	}
	frameTokens := p.Billing.tokens(shape.Edge, shape.Edge)
	if !p.Billing.Rule.Verified {
		frameTokens = max(frameTokens, ompDefaultFrameTokens)
	}
	edgeTokens := int64(math.Ceil(float64(2*ompGridFor(shape).capacity()) * 1.15 / 4))
	tokens := options.HistoryTokens - edgeTokens - 2000
	frameBytes := ompFrameBase64Estimate
	if shape.Name == "8on22-bw" || shape.Name == "11on16-bw" {
		frameBytes = min(frameBytes, int(math.Ceil(float64(frameBytes)*math.Pow(float64(shape.Edge)/1932, 2))))
	}
	budget := int64(p.FrameBytes)
	if options.MaxImageBytes > 0 {
		budget = min(budget, int64(base64.StdEncoding.EncodedLen(int(options.MaxImageBytes))))
	}
	return min(limit, p.MaxFrames, max(1, int(tokens/int64(frameTokens))), max(1, int(budget)/frameBytes))
}

func ompPrevious(options OMPOptions) (string, int) {
	prior := options.Previous
	if prior == nil {
		return "", 0
	}
	source := prior.Text
	elided := prior.Truncated
	if source == "" {
		parts := make([]string, 0, 2)
		for _, text := range []string{prior.Head, prior.Tail} {
			if text != "" {
				parts = append(parts, text)
			}
		}
		for _, frame := range prior.Frames {
			elided += frame.Characters
		}
		source = strings.Join(parts, string(ompNewline))
	}
	source = ompElideData(source, true)
	if !options.IncludeThinking {
		source = ompStripThinking(source)
	}
	return source, elided
}

func ompStripThinking(text string) string {
	var out strings.Builder
	dim, omit := false, false
	start := true
	for i, r := range text {
		if start && !dim && r == '¶' {
			for _, role := range []string{"user", "think", "ai", "call"} {
				if strings.HasPrefix(text[i:], "¶"+role+":") {
					omit = role == "think"
					break
				}
			}
		}
		if !omit {
			out.WriteRune(r)
		}
		if r == ompDimOn || r == ompDimOff {
			dim = r == ompDimOn
		}
		start = r == '\n' || r == ompNewline
	}
	if omit {
		return strings.TrimRight(out.String(), "\n█")
	}
	return out.String()
}

func ompFinish(ctx context.Context, layout ompLayout, shape OMPShape, billing ompBilling, options OMPOptions, truncated int, hasDigest bool) (OMPArchive, error) {
	out := OMPArchive{
		Shape: shape, Text: layout.kept, Head: layout.head, Tail: layout.tail,
		Truncated: truncated + layout.truncated, ImageTokensVerified: billing.Rule.Verified,
		TokenBasis: "OMP SnapCompact " + OMPVersion + " model catalog estimate",
	}
	dim := strings.LastIndexByte(out.Head, ompDimOn) > strings.LastIndexByte(out.Head, ompDimOff)
	var encoded int64
	for _, page := range layout.frames {
		text := page.text
		if dim {
			text = string(ompDimOn) + text
		}
		dim = strings.LastIndexByte(text, ompDimOn) > strings.LastIndexByte(text, ompDimOff)
		if page.shape.DimStopwords {
			text = ompDimWords(text)
		}
		frame, err := RenderOMPFrame(ctx, text, page.shape)
		if err != nil {
			return OMPArchive{}, err
		}
		frame.Detail = billing.ImageDetail
		encoded += int64(len(frame.PNG))
		if options.MaxImageBytes > 0 && encoded > options.MaxImageBytes {
			return OMPArchive{}, ErrRenderLimit
		}
		out.Frames = append(out.Frames, frame)
		out.ImageTokens += int64(billing.tokens(frame.Width, frame.Height))
		out.Characters += frame.Characters
	}
	if dim && out.Tail != "" {
		out.Text = strings.TrimSuffix(out.Text, out.Tail) + string(ompDimOn) + out.Tail
		out.Tail = string(ompDimOn) + out.Tail
	}
	out.Characters += ompUTF16Len(out.Head) + ompUTF16Len(out.Tail)
	out.ReadFiles, out.ModifiedFiles = options.Files.Lists()
	out.Summary = ompSummary(out, options.IncludeThinking, hasDigest, ompFileList(out.ReadFiles, out.ModifiedFiles, options.Files.Read))
	return out, nil
}

func ompSummary(a OMPArchive, think, hasDigest bool, files string) string {
	if a.Head == "" && a.Tail == "" && len(a.Frames) == 0 && files == "" {
		return "No prior history."
	}
	var b strings.Builder
	b.WriteString("Resume prior conversation. Earlier turns archived under HISTORY below, oldest→newest. Read HISTORY fully; continue the live conversation following it.\n\nArchived transcript scopes:\n")
	if think {
		b.WriteString("- `¶user:`, `¶think:`, `¶ai:`, `¶call:`: user, assistant reasoning, assistant reply, tool call.\n")
	} else {
		b.WriteString("- `¶user:`, `¶ai:`, `¶call:`: user, assistant reply, tool call.\n")
	}
	b.WriteString("- Unprefixed following lines: current scope. Consecutive same-kind blocks omit repeated prefix.\n- Tool call: `¶call:name(args)//intent`; trailing `//intent` optional. `<out>…</out>`: tool output.\n\nReading HISTORY:\n- Plain text: verbatim transcript; rely on it exactly.\n")
	if len(a.Frames) != 0 {
		b.WriteString("- Some middle sections: images, not text. Each image: one page of that transcript, in reading order between marked delimiters. Solid black cell: newline; runs of spaces collapse to one.\n")
		cols := []string{}
		for _, f := range a.Frames {
			val := strconv.Itoa(f.Cols)
			found := false
			for _, c := range cols {
				found = found || c == val
			}
			if !found {
				cols = append(cols, val)
			}
		}
		if a.Shape.Doc {
			fmt.Fprintf(&b, "  - Frame: two side-by-side columns, each %s characters wide, up to %d rows tall; read left top→bottom, then right.\n", strings.Join(cols, " or "), ompGridFor(a.Shape).rows)
		} else {
			fmt.Fprintf(&b, "  - Frame: one grid %s characters wide, up to %d rows tall; read left→right, top→bottom. No word wrap; words may break across rows.\n", strings.Join(cols, " or "), ompGridFor(a.Shape).rows)
		}
		if a.Shape.SentenceInk {
			b.WriteString("  - Ink: six colors, one per sentence.\n")
		}
		if a.Shape.DimStopwords {
			b.WriteString("  - Function words: dim gray; content words: full ink.\n")
		}
		if a.Shape.Repeat > 1 {
			b.WriteString("  - Each line printed twice (white, then pale-yellow band); copies identical.\n")
		}
	}
	if hasDigest {
		b.WriteString("- HISTORY opens with a condensed digest of still-older context predating archived turns.\n")
	}
	if a.Truncated > 0 {
		fmt.Fprintf(&b, "- About %d characters of older middle history dropped to fit archive budget.\n", a.Truncated)
	}
	b.WriteString("- If an exact earlier detail matters and a section is unclear, re-derive from workspace (re-read files, re-run commands), rather than guess.\n\n")
	if files != "" {
		b.WriteString("FILES\n===================\n" + files + "\n")
	}
	b.WriteString("HISTORY\n===================")
	return b.String()
}

func ompPlain(text string) string {
	return ompElideData(strings.ReplaceAll(ompRemoveDim.Replace(text), string(ompNewline), "\n"), true)
}

// HistoryText exposes the retained archive source for preview and readback.
func (a OMPArchive) HistoryText() string { return ompPlain(a.Text) }

// Blocks exposes the chronological text and image blocks of the archive.
func (a OMPArchive) Blocks() []OMPBlock {
	out := make([]OMPBlock, 0, 2+len(a.Frames))
	if a.Head != "" {
		head := ompPlain(a.Head)
		if len(a.Frames) > 0 {
			head += "\n-------------- imaged middle below\n"
		}
		out = append(out, OMPBlock{Text: head})
	}
	for i := range a.Frames {
		out = append(out, OMPBlock{Frame: &a.Frames[i]})
	}
	if a.Tail != "" {
		tail := ompPlain(a.Tail)
		switch {
		case len(a.Frames) > 0:
			tail = "-------------- imaged middle above\n" + tail
		case a.Truncated > 0:
			tail = "\n-------------- middle history omitted above\n" + tail
		}
		if len(out) > 0 && out[len(out)-1].Frame == nil {
			out[len(out)-1].Text += tail
		} else {
			out = append(out, OMPBlock{Text: tail})
		}
	}
	return out
}
