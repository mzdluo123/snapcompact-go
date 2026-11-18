package snapcompact

import (
	"fmt"
	"math"
	"regexp"
	"strings"
)

type archiveShape struct {
	name               string
	width, pitch, edge int
}
type archiveBilling struct {
	kind, basis string
	known       bool
	cap         int
}

var highClaude = regexp.MustCompile(`(?i)claude.*(?:opus[-.](?:4[-.](?:[7-9]|[1-9][0-9])|[5-9](?:[-.]\d+)?)(?:\D|$)|(?:fable|mythos))`)
var patchOpenAI = regexp.MustCompile(`(?:gpt|codex)[-.](5[.-][2-9]|6(?:[.-][0-9]+)?)(?:[^0-9]|$)`)
var verifiedOpenAI = regexp.MustCompile(`(?:gpt|codex)[-.](?:5[.-][5-9]|6(?:[.-][01])?)(?:[^0-9.]|$)`)
var standardClaude = regexp.MustCompile(`claude.*(?:opus|sonnet|haiku)[-.](?:[1-3](?:[-.]\d+)?|4[-.]?[0-6])(?:[^0-9]|$)`)
var gemini3 = regexp.MustCompile(`gemini[-.]3(?:[^0-9]|$)`)

// Geometry follows the pinned upstream model families. Billing remains unknown
// unless a model lineage and the default transport policy are both identified.
func archiveModel(model string, maxEdge int) (archiveShape, archiveBilling) {
	id := strings.ToLower(model)
	s := archiveShape{"8on22-bw", 8, 22, 1568}
	b := archiveBilling{"ceiling", "unknown model/transport; conservative 6000-token planning allowance per image (not a billing guarantee)", false, 0}
	switch {
	case strings.Contains(id, "claude"):
		s = archiveShape{"11on16-bw", 11, 16, 1568}
		if highClaude.MatchString(id) {
			s.edge = 1932
		}
		b = archiveBilling{"anthropic", "pinned catalog anthropic-patch: ceil(width/28)*ceil(height/28), capped at 4784; default transport estimate", highClaude.MatchString(id), 4784}
		if standardClaude.MatchString(id) {
			b.cap = 1568
			b.known = strings.Contains(id, "sonnet-4-6") || strings.Contains(id, "sonnet-4.6") || strings.Contains(id, "sonnet-46")
			b.basis = "pinned catalog standard Claude 28px patches, 1568-token ceiling; provider may downscale"
		}
	case strings.Contains(id, "gemini"):
		s.edge = 2048
		if gemini3.MatchString(id) {
			b = archiveBilling{"gemini", "pinned catalog Google/Gemini fixed 1120 tokens/image at default media resolution; unverified transport estimate", false, 0}
		}
	case strings.Contains(id, "glm"):
		s = archiveShape{"8on16-bw", 8, 16, 1568}
	case strings.Contains(id, "gpt") || strings.Contains(id, "codex"):
		if patchOpenAI.MatchString(id) || strings.Contains(id, "gpt-6-astra") {
			b = archiveBilling{"openai", "pinned catalog 32px patches * 1.2; transport default auto (high/original both preserve this <=1568px square); catalog estimate, not measured here", verifiedOpenAI.MatchString(id) || strings.Contains(id, "gpt-6-astra"), 0}
		}
	}
	if maxEdge > 0 {
		s.edge = min(s.edge, maxEdge)
	}
	return s, b
}

func (b archiveBilling) tokens(width, height int) int64 {
	switch b.kind {
	case "openai":
		patches := int64((width+31)/32) * int64((height+31)/32)
		return (patches*6 + 4) / 5
	case "anthropic":
		return min(int64(b.cap), int64((width+27)/28)*int64((height+27)/28))
	case "gemini":
		return 1120
	}
	return 6000
}

func archiveConfig(c ArchiveConfig) (ArchiveConfig, error) {
	if c.MaxPages == 0 {
		c.MaxPages = 8
	}
	if c.MaxImageBytes == 0 {
		c.MaxImageBytes = 32 << 20
	}
	if c.MaxPages < 1 || c.MaxPages > 80 || c.MaxEdge < 0 || c.MaxEdge > 4096 || (c.MaxEdge > 0 && c.MaxEdge < 64) || c.MaxImageBytes < 1 || c.MaxImageBytes > 128<<20 || c.MaxImageTokens < 0 || c.MaxImageTokens > math.MaxInt64/2 || c.MaxTextBytes < 0 || c.MaxTextBytes > 16<<20 {
		return c, fmt.Errorf("%w: archive limits", ErrRenderInput)
	}
	return c, nil
}
