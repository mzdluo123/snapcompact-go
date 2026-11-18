package snapcompact

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"image/png"
	"os"
	"testing"
)

type ompGolden struct {
	Shapes []struct {
		Name, Text, Pixels string
		Cols, Rows, Chars  int
	}
	Archives []struct {
		Name, Summary, Head, Tail, Text string
		Messages                        []OMPMessage
		FrameLimit, Chars, Truncated    int
		All                             []struct{ Type, Text, Detail, Pixels string }
	}
}

func ompGoldens(t *testing.T) ompGolden {
	t.Helper()
	file, err := os.Open("testdata/omp-18.22.2.json.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	input, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	var fixtures ompGolden
	if err := json.NewDecoder(input).Decode(&fixtures); err != nil {
		t.Fatal(err)
	}
	return fixtures
}

func ompPixelHash(t *testing.T, data []byte) string {
	t.Helper()
	pixels, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	w, h := pixels.Bounds().Dx(), pixels.Bounds().Dy()
	hash := sha256.New()
	row := make([]byte, w*3)
	for y := range h {
		for x := range w {
			r, g, b, _ := pixels.At(x, y).RGBA()
			copy(row[x*3:], []byte{byte(r >> 8), byte(g >> 8), byte(b >> 8)})
		}
		_, _ = hash.Write(row)
	}
	return fmt.Sprintf("%dx%d:%x", w, h, hash.Sum(nil))
}

func TestOMPReferenceShapePixels(t *testing.T) {
	for _, fixture := range ompGoldens(t).Shapes {
		t.Run(fixture.Name, func(t *testing.T) {
			shape := ompShapes[fixture.Name]
			shape.Edge = 256
			frame, err := RenderOMPFrame(context.Background(), fixture.Text, shape)
			if err != nil {
				t.Fatal(err)
			}
			if frame.Cols != fixture.Cols || frame.Rows != fixture.Rows || frame.Characters != fixture.Chars {
				t.Fatalf("geometry=%dx%d, characters=%d", frame.Cols, frame.Rows, frame.Characters)
			}
			if got := ompPixelHash(t, frame.PNG); got != fixture.Pixels {
				t.Fatalf("pixels %s want %s", got, fixture.Pixels)
			}
		})
	}
}

func TestOMPReferenceArchive(t *testing.T) {
	for _, fixture := range ompGoldens(t).Archives {
		t.Run(fixture.Name, func(t *testing.T) {
			actual, err := CompactOMP(context.Background(), fixture.Messages, OMPOptions{
				Target: OMPTargetForModel("openai-codex/gpt-6-astra"), MaxFrames: fixture.FrameLimit,
			})
			if err != nil {
				t.Fatal(err)
			}
			if actual.Summary != fixture.Summary || actual.Head != fixture.Head ||
				actual.Tail != fixture.Tail || actual.Text != fixture.Text ||
				actual.Characters != fixture.Chars || actual.Truncated != fixture.Truncated {
				t.Fatalf("archive differs from upstream: characters=%d, truncated=%d", actual.Characters, actual.Truncated)
			}
			blocks := actual.Blocks()
			if len(blocks) != len(fixture.All) {
				t.Fatalf("%d blocks, want %d", len(blocks), len(fixture.All))
			}
			for i, reference := range fixture.All {
				block := blocks[i]
				if reference.Type == "text" {
					if block.Frame != nil || block.Text != reference.Text {
						t.Fatalf("block %d differs from upstream text", i)
					}
				} else if block.Frame == nil || block.Frame.Detail != reference.Detail ||
					ompPixelHash(t, block.Frame.PNG) != reference.Pixels {
					t.Fatalf("block %d differs from upstream image", i)
				}
			}
		})
	}
}
