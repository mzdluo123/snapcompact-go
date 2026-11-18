package snapcompact

import (
	"bytes"
	"context"
	"errors"
	"image/png"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/rivo/uniseg"
)

func archiveForTest(t *testing.T) ArchiveRenderer {
	t.Helper()
	r, err := NewArchiveRenderer()
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func assertArchiveCoverage(t *testing.T, p Projection, a Archive) {
	t.Helper()
	seen := make(map[string][]bool)
	boundaries := make(map[string]map[int]bool)
	total, retained := 0, 0
	for _, s := range p.Segments {
		n := utf8.RuneCountInString(s.Text)
		total += n
		seen[s.SourceID] = make([]bool, n)
		cuts := map[int]bool{0: true}
		g := uniseg.NewGraphemes(s.Text)
		at := 0
		for g.Next() {
			at += utf8.RuneCountInString(g.Str())
			cuts[at] = true
		}
		boundaries[s.SourceID] = cuts
	}
	add := func(ranges []Range, kept bool) {
		for _, r := range ranges {
			if r.Start == r.End {
				continue
			}
			body, exists := seen[r.SourceID]
			if !exists || r.Field != "projection" || r.Start < 0 || r.End > len(body) || r.Start > r.End {
				t.Fatalf("invalid range %+v", r)
			}
			if !boundaries[r.SourceID][r.Start] || !boundaries[r.SourceID][r.End] {
				t.Fatalf("split grapheme %+v", r)
			}
			for i := r.Start; i < r.End; i++ {
				if body[i] {
					t.Fatalf("overlapping coverage %+v", r)
				}
				body[i] = true
			}
			if kept {
				retained += r.End - r.Start
			}
		}
	}
	add(a.TextRanges, true)
	for i, page := range a.Render.Pages {
		if page.Index != i {
			t.Fatalf("page index %d != %d", page.Index, i)
		}
		add(page.Ranges, true)
		img, err := png.Decode(bytes.NewReader(page.PNG))
		if err != nil {
			t.Fatal(err)
		}
		if img.Bounds().Dx() != page.Width || img.Bounds().Dy() != page.Height {
			t.Fatal("PNG dimensions differ")
		}
	}
	add(a.Omitted, false)
	for id, body := range seen {
		for i, covered := range body {
			if !covered {
				t.Fatalf("gap %s:%d", id, i)
			}
		}
	}
	if a.SourceRunes != total || a.RetainedRunes != retained {
		t.Fatalf("counts %d/%d != %d/%d", a.SourceRunes, a.RetainedRunes, total, retained)
	}
	if len(a.Render.Uncovered) != 0 {
		t.Fatal("successful archive has uncovered ranges")
	}
}

func TestArchiveShortHistoryAndIdentity(t *testing.T) {
	p := Projection{Segments: []Segment{
		{SourceID: "one", Role: "assistant", Text: "first\n  exact code"},
		{SourceID: "two", Role: "assistant", ToolName: "read", CallID: "call1", Text: "input"},
		{SourceID: "three", Role: "tool", ToolName: "read", CallID: "call1", Text: "output"},
		{SourceID: "four", Role: "tool", Text: "more"},
	}}
	a, err := archiveForTest(t).Compact(context.Background(), p, ArchiveConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Render.Pages) != 0 || a.TextTail != "" || a.TextHead != a.HistoryText {
		t.Fatal("short archive must be all text")
	}
	if strings.Count(a.HistoryText, "¶call:") != 1 || strings.Count(a.HistoryText, `"read"`) != 1 || !strings.Contains(a.HistoryText, "[s3]→s2") {
		t.Fatalf("identity aggregation: %q", a.HistoryText)
	}
	if !strings.Contains(a.HistoryText, "first\n  exact code") {
		t.Fatal("source changed")
	}
	assertArchiveCoverage(t, p, a)
}

func TestArchiveMixedCoverageAndFoveation(t *testing.T) {
	// The newest dense page can be partial: 400 repetitions leave 112 cells,
	// while 425 align the dense middle to complete 128-cell pages.
	for _, tc := range []struct {
		repetitions, denseHeight int
	}{{400, 112}, {425, 128}} {
		p := Projection{Segments: []Segment{{SourceID: "source", Role: "user", Text: "BEGIN" + strings.Repeat("0123456789abcdef", tc.repetitions) + "END"}}}
		a, err := archiveForTest(t).Compact(context.Background(), p, ArchiveConfig{Model: "gpt-6-astra", MaxEdge: 128, MaxPages: 5})
		if err != nil {
			t.Fatal(err)
		}
		assertArchiveCoverage(t, p, a)
		if !strings.Contains(a.TextHead, "BEGIN") || !strings.HasSuffix(a.TextTail, "END") || len(a.Omitted) == 0 || len(a.Render.Pages) != 5 {
			t.Fatal("foveated edges not retained")
		}
		for i, height := range []int{110, 110, tc.denseHeight, 110, 64} {
			if a.Render.Pages[i].Height != height {
				t.Fatalf("%d repetitions: page %d height = %d, want %d", tc.repetitions, i, a.Render.Pages[i].Height, height)
			}
		}
		// All imaged content here is ASCII. This independently proves the
		// dense page's 8on16 packing rather than merely inspecting PNG height.
		denseRunes := 0
		for _, coverage := range a.Render.Pages[2].Ranges {
			denseRunes += coverage.End - coverage.Start
		}
		if denseRunes != tc.denseHeight {
			t.Fatalf("dense page retains %d ASCII cells, want %d", denseRunes, tc.denseHeight)
		}
		if !a.TokenEstimateKnown || a.ImageTokens <= 0 {
			t.Fatal("catalog lineage estimate missing")
		}
	}
}

func TestArchiveTextEdgeExactByteBoundaries(t *testing.T) {
	const text = "a界e\u0301Z"
	for _, tc := range []struct {
		limit      int
		head, tail string
	}{
		{0, "", ""}, {1, "", "Z"}, {2, "a", "Z"}, {3, "a", "Z"},
		{4, "a", "Z"}, {5, "a", "e\u0301Z"}, {6, "a", "e\u0301Z"},
		{7, "a", "e\u0301Z"}, {8, "a界", "e\u0301Z"},
	} {
		head, tail := archiveEdges(context.Background(), text, 100, 100, tc.limit)
		if text[:head] != tc.head || text[tail:] != tc.tail {
			t.Fatalf("limit %d: edges %q/%q, want %q/%q", tc.limit, text[:head], text[tail:], tc.head, tc.tail)
		}
		if head+len(text)-tail > tc.limit {
			t.Fatalf("limit %d exceeded", tc.limit)
		}
	}
}

func TestArchiveUnicodeAndBudgets(t *testing.T) {
	r := archiveForTest(t)
	p := Projection{Segments: []Segment{{SourceID: "unicode", Role: "user", Text: strings.Repeat("汉字 e\u0301\n  code ", 140)}}}
	for _, cfg := range []ArchiveConfig{
		{MaxEdge: 128, MaxPages: 8, MaxTextBytes: 63},
		{MaxEdge: 128, MaxPages: 8, MaxTextBytes: 63, MaxImageTokens: 1},
		{MaxEdge: 128, MaxPages: 8, MaxTextBytes: 63, MaxImageBytes: 1},
	} {
		a, err := r.Compact(context.Background(), p, cfg)
		if err != nil {
			t.Fatal(err)
		}
		assertArchiveCoverage(t, p, a)
		if len(a.TextHead)+len(a.TextTail) > 63 {
			t.Fatal("edge byte cap exceeded")
		}
		if (cfg.MaxImageBytes == 1 || cfg.MaxImageTokens == 1) && (len(a.Render.Pages) != 0 || len(a.Omitted) == 0) {
			t.Fatal("strict budget not applied")
		}
	}
}

func TestArchiveMissingGlyphOnlyInImages(t *testing.T) {
	r := archiveForTest(t)
	p := Projection{Segments: []Segment{{SourceID: "s", Role: "user", Text: "\U0010ffff"}}}
	if _, err := r.Compact(context.Background(), p, ArchiveConfig{}); err != nil {
		t.Fatal("text-only glyph rejected", err)
	}
	p.Segments[0].Text = strings.Repeat("a", 100) + "\U0010ffff" + strings.Repeat("b", 100)
	a, err := r.Compact(context.Background(), p, ArchiveConfig{MaxEdge: 128, MaxPages: 8})
	if !errors.Is(err, ErrMissingGlyph) || len(a.Render.Missing) == 0 || len(a.Render.Uncovered) == 0 || len(a.Render.Pages) != 0 {
		t.Fatalf("missing glyph result: %+v %v", a, err)
	}
}

func TestArchiveGridBoundaryAndNewline(t *testing.T) {
	r := archiveForTest(t).(*archiveRenderer)
	g, err := r.grid()
	if err != nil {
		t.Fatal(err)
	}
	defer g.face.Close()
	shape := archiveShape{"8on16-bw", 8, 16, 128}
	for _, size := range []int{127, 128, 129} {
		frames, err := g.paginate(context.Background(), strings.Repeat("x", size), 0, size, shape, false)
		if err != nil {
			t.Fatal(err)
		}
		if len(frames) != (size+127)/128 {
			t.Fatal("boundary page count", size, frames)
		}
		if frames[0].end != min(size, 128) {
			t.Fatal("grid underfilled")
		}
	}
	canvas, err := g.draw(context.Background(), "\n", shape, 64)
	if err != nil {
		t.Fatal(err)
	}
	if canvas.GrayAt(0, 1).Y != 0 || canvas.GrayAt(7, 13).Y != 0 || canvas.GrayAt(8, 1).Y != 255 {
		t.Fatal("newline is not FULL BLOCK cell")
	}
}

func TestArchiveCancellationAndLimits(t *testing.T) {
	r := archiveForTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.Compact(ctx, Projection{}, ArchiveConfig{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	for _, cfg := range []ArchiveConfig{{MaxPages: 81}, {MaxPages: -1}, {MaxEdge: 63}, {MaxImageBytes: -1}, {MaxTextBytes: -1}} {
		if _, err := r.Compact(context.Background(), Projection{}, cfg); !errors.Is(err, ErrRenderInput) {
			t.Fatal("invalid config accepted", cfg, err)
		}
	}
}

func TestArchiveModelCatalog(t *testing.T) {
	for _, tc := range []struct {
		model string
		edge  int
		known bool
	}{
		{"gpt-6-astra", 1568, true}, {"gpt-5.5", 1568, true}, {"gpt-5.4", 1568, false},
		{"gpt-6.8", 1568, false}, {"unclassified", 1568, false}, {"claude-opus-4-7", 1932, true},
		{"claude-sonnet-4-6", 1568, true}, {"gemini-3-pro", 2048, false},
	} {
		s, b := archiveModel(tc.model, 0)
		if s.edge != tc.edge || b.known != tc.known {
			t.Fatalf("%s: %+v %+v", tc.model, s, b)
		}
	}
	_, b := archiveModel("gpt-6-astra", 0)
	if b.tokens(1568, 1568) != 2882 {
		t.Fatal("patch count")
	}
	s, _ := archiveModel("gpt-6-astra", 1024)
	if s.edge != 1024 {
		t.Fatal("host max edge")
	}
}
