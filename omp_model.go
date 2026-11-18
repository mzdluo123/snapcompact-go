package snapcompact

import (
	_ "embed"
	"encoding/json"
	"math"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

var ompShapes = map[string]OMPShape{
	"8x8r-bw":            {"8x8r-bw", "8x8", 8, 8, 2, 1568, false, false, false, false},
	"8x8r-sent":          {"8x8r-sent", "8x8", 8, 8, 2, 1568, true, false, false, false},
	"8x8u-bw":            {"8x8u-bw", "8x8", 8, 8, 1, 1568, false, false, false, false},
	"8x8u-sent":          {"8x8u-sent", "8x8", 8, 8, 1, 1568, true, false, false, false},
	"6x6u-bw":            {"6x6u-bw", "8x8", 6, 6, 1, 1568, false, false, false, true},
	"6x6u-sent":          {"6x6u-sent", "8x8", 6, 6, 1, 1568, true, false, false, true},
	"5x8-bw":             {"5x8-bw", "5x8", 5, 8, 1, 2576, false, false, false, false},
	"5x8-sent":           {"5x8-sent", "5x8", 5, 8, 1, 2576, true, false, false, false},
	"6x12-dim":           {"6x12-dim", "6x12", 6, 12, 1, 1568, false, true, false, false},
	"8x13-bw":            {"8x13-bw", "8x13", 8, 13, 1, 1568, false, false, false, false},
	"8on16-bw":           {"8on16-bw", "8x13", 8, 16, 1, 1568, false, false, false, false},
	"8on22-bw":           {"8on22-bw", "8x13", 8, 22, 1, 1568, false, false, false, false},
	"11on16-bw":          {"11on16-bw", "8x13", 11, 16, 1, 1568, false, false, false, false},
	"silver16-bw":        {"silver16-bw", "silver", 16, 16, 1, 1568, false, false, false, false},
	"doc-8on16-bw":       {"doc-8on16-bw", "8x13", 8, 16, 1, 1568, false, false, true, false},
	"doc-8on16-sent":     {"doc-8on16-sent", "8x13", 8, 16, 1, 1568, true, false, true, false},
	"doc-8on16-sent-dim": {"doc-8on16-sent-dim", "8x13", 8, 16, 1, 1568, true, true, true, false},
}

const ompDefaultFrameTokens = 5024
const ompFrameBase64Estimate = 170000

//go:embed omp/catalog.json
var ompCatalogData []byte

type ompCatalog struct {
	Profiles         []ompProfile
	Provider, Models map[string]ompRoute
	Bare, API        map[string]int
}

type ompRoute struct {
	API     string
	Profile int
}

type ompProfile struct {
	Name                        string
	Edge, MaxFrames, FrameBytes int
	Billing                     ompBilling
}

type ompBilling struct {
	Rule        ompTokenRule
	ImageDetail string
}

type ompPatchSizing struct {
	MaxEdge, PatchBudget int
}

type ompTokenRule struct {
	Regime, Auto                                                 string
	Verified                                                     bool
	Multiplier                                                   float64
	Low, High, Original                                          ompPatchSizing
	MaxEdge, MaxTokens, Tokens, PatchSize, Downsample, MinPixels int
}

var ompReadCatalog = sync.OnceValue(func() ompCatalog {
	var result ompCatalog
	if err := json.Unmarshal(ompCatalogData, &result); err != nil {
		panic(err)
	}
	return result
})

// OMPTargetForModel recognizes a provider prefix as OMP writes it in model IDs.
func OMPTargetForModel(id string) OMPTarget {
	if provider, model, ok := strings.Cut(id, "/"); ok {
		if route, ok := ompReadCatalog().Provider[provider]; ok {
			return OMPTarget{Model: model, Provider: provider, API: route.API}
		}
	}
	return OMPTarget{Model: id}
}

var ompClaudeOpus = regexp.MustCompile(`(?:opus[-.]?)([0-9]+)(?:[-.]([0-9]+))?`)
var ompOpenAIRevision = regexp.MustCompile(`(?:gpt|codex)[-.]([0-9]+)(?:[-.]([0-9]+))?`)

func ompResolveProfile(target OMPTarget) ompProfile {
	c := ompReadCatalog()
	if route, ok := c.Models[target.Provider+"/"+target.Model]; ok && (target.API == "" || target.API == route.API) {
		return c.Profiles[route.Profile]
	}
	index, ok := c.API[target.API]
	if !ok {
		index = c.API[""]
	}
	profile := c.Profiles[index]
	if route, ok := c.Provider[target.Provider]; ok && (target.API == "" || target.API == route.API) {
		profile = c.Profiles[route.Profile]
	}
	if i, ok := c.Bare[target.Model]; ok {
		p := c.Profiles[i]
		profile.Name, profile.Edge = p.Name, p.Edge
		if p.Billing.Rule.Regime != "" {
			profile.Billing.Rule = p.Billing.Rule
		}
	} else {
		profile = ompUnlistedProfile(profile, strings.ToLower(target.Model))
	}
	if target.Provider == "cursor" && (strings.Contains(strings.ToLower(target.Model), "claude") || strings.Contains(strings.ToLower(target.Model), "gpt")) {
		profile.Edge = min(profile.Edge, 1024)
	}
	return profile
}

func ompUnlistedProfile(p ompProfile, id string) ompProfile {
	switch {
	case strings.Contains(id, "claude"):
		p.Name, p.Edge = "11on16-bw", 1568
		rule := ompTokenRule{Regime: "anthropic-patch", MaxEdge: 2576, MaxTokens: 4784, Verified: true}
		switch {
		case strings.Contains(id, "mythos") || strings.Contains(id, "fable"):
			p.Edge = 1932
		case strings.Contains(id, "opus"):
			if m := ompClaudeOpus.FindStringSubmatch(id); m != nil {
				major, _ := strconv.Atoi(m[1])
				minor, _ := strconv.Atoi(m[2])
				if major == 4 && minor >= 7 || major >= 5 && major < 10 || major >= 47 {
					p.Edge = 1932
				} else {
					rule.MaxEdge, rule.MaxTokens, rule.Verified = 1568, 1568, false
				}
			}
		case strings.Contains(id, "sonnet"), strings.Contains(id, "haiku"):
			rule.MaxEdge, rule.MaxTokens = 1568, 1568
			rule.Verified = strings.Contains(id, "sonnet-4.6") || strings.Contains(id, "sonnet-4-6") || strings.Contains(id, "sonnet-46")
		}
		p.Billing.Rule = rule
	case strings.Contains(id, "gemini"):
		p.Name, p.Edge = "8on22-bw", 2048
		if strings.Contains(id, "gemini-3") {
			p.Billing.Rule = ompTokenRule{Regime: "fixed", Tokens: 1120}
		}
	case strings.Contains(id, "gpt"), strings.Contains(id, "codex"):
		p.Name, p.Edge = "8on22-bw", 1568
		if m := ompOpenAIRevision.FindStringSubmatch(id); m != nil {
			major, _ := strconv.Atoi(m[1])
			minor, _ := strconv.Atoi(m[2])
			rule := ompReadCatalog().Profiles[ompReadCatalog().API["openai-responses"]].Billing.Rule
			switch {
			case major == 6 || major == 5 && minor >= 6:
				rule.Original = ompPatchSizing{MaxEdge: 65535}
				rule.Verified = major == 5 || minor < 2
			case major == 5 && minor == 5:
				rule.Verified = true
			case major == 5 && minor == 4:
				rule.Auto = "high"
			case major == 5 && (minor == 2 || minor == 3):
				rule.Auto = "high"
				rule.Original, rule.High = ompPatchSizing{2048, 6144}, ompPatchSizing{2048, 6144}
			default:
				return p
			}
			p.Billing.Rule = rule
		}
	case strings.Contains(id, "glm"):
		p.Name, p.Edge = "8on16-bw", 1568
	case strings.Contains(id, "kimi"):
		p.Name, p.Edge = "8on22-bw", 1568
	}
	return p
}

func ompShapeForText(text string, target OMPTarget, profile ompProfile, variant string) (OMPShape, ompScan, error) {
	name := profile.Name
	auto := variant == "" || variant == "auto"
	if !auto {
		name = variant
	}
	shape, ok := ompShapes[name]
	if !ok {
		return OMPShape{}, ompScan{}, ErrRenderInput
	}
	if auto {
		shape.Edge = profile.Edge
	}
	if target.MaxEdge > 0 {
		shape.Edge = min(shape.Edge, target.MaxEdge)
	}
	providerEdge := profile.Edge
	if !auto && target.Provider == "cursor" && providerEdge == 1024 {
		shape.Edge = min(shape.Edge, providerEdge)
	}
	scan := ompNormalize(text, shape.Font)
	if auto && (!scan.safe() || ompCJKHeavy(text)) {
		silver := ompShapes["silver16-bw"]
		if target.MaxEdge > 0 {
			silver.Edge = min(silver.Edge, target.MaxEdge)
		}
		if target.Provider == "cursor" && providerEdge == 1024 {
			silver.Edge = min(silver.Edge, providerEdge)
		}
		if silverScan := ompNormalize(text, silver.Font); silverScan.safe() {
			return silver, silverScan, nil
		}
	}
	return shape, scan, nil
}

func ompCeilDiv(n, d int) int { return (n + d - 1) / d }

func (b ompBilling) tokens(width, height int) int {
	r := b.Rule
	switch r.Regime {
	case "fixed":
		return r.Tokens
	case "openai-patch":
		return ompOpenAITokens(r, b.ImageDetail, width, height)
	case "anthropic-patch":
		return ompAnthropicTokens(r, width, height)
	case "deepseek-patch":
		return ompDeepSeekTokens(r, width, height)
	default:
		return ompDefaultFrameTokens
	}
}

func ompOpenAITokens(r ompTokenRule, detail string, w, h int) int {
	if detail == "" || detail == "auto" {
		detail = r.Auto
	}
	p := r.Original
	switch detail {
	case "high":
		p = r.High
	case "low":
		p = r.Low
	}
	if edge := max(w, h); edge > p.MaxEdge {
		scale := float64(p.MaxEdge) / float64(edge)
		w, h = max(1, int(math.Round(float64(w)*scale))), max(1, int(math.Round(float64(h)*scale)))
	}
	n := ompCeilDiv(w, 32) * ompCeilDiv(h, 32)
	if p.PatchBudget > 0 && n > p.PatchBudget {
		scale := math.Sqrt(float64(32*32*p.PatchBudget) / float64(w*h))
		x, y := float64(w)*scale/32, float64(h)*scale/32
		scale *= min(math.Floor(x)/x, math.Floor(y)/y)
		w, h = int(math.Floor(float64(w)*scale)), int(math.Floor(float64(h)*scale))
		n = p.PatchBudget
		if w > 0 && h > 0 {
			n = min(n, ompCeilDiv(w, 32)*ompCeilDiv(h, 32))
		}
	}
	return int(math.Ceil(float64(n) * r.Multiplier))
}

func ompAnthropicTokens(r ompTokenRule, w, h int) int {
	long, short := max(w, h), min(w, h)
	aspect := float64(long) / float64(short)
	fits := func(l, s int) bool {
		a, b := ompCeilDiv(l, 28), ompCeilDiv(s, 28)
		return a*28 <= r.MaxEdge && b*28 <= r.MaxEdge && a*b <= r.MaxTokens
	}
	other := func(l int) int { return max(1, int(math.RoundToEven(float64(l)/aspect))) }
	if !fits(long, short) {
		lo, hi := 1, long
		for lo+1 < hi {
			m := (lo + hi) / 2
			if fits(m, other(m)) {
				lo = m
			} else {
				hi = m
			}
		}
		long, short = lo, other(lo)
	}
	return ompCeilDiv(long, 28) * ompCeilDiv(short, 28)
}

func ompDeepSeekTokens(r ompTokenRule, w, h int) int {
	cell := r.PatchSize * r.Downsample
	count := func(w, h int) int { return ompCeilDiv(h, cell)*(ompCeilDiv(w, cell)+1) + 2 }
	if w*h < r.MinPixels {
		scale := math.Sqrt(float64(r.MinPixels) / float64(w*h))
		w, h = int(float64(w)*scale), int(float64(h)*scale)
	}
	if n := count(w, h); n <= r.MaxTokens {
		return n
	}
	aspect := float64(h) / float64(w)
	cols := math.Sqrt(float64(r.MaxTokens-2)/aspect+0.25) - 0.5
	rows := cols * aspect
	switch {
	case cols < 1:
		return count(cell, ((r.MaxTokens-2)/2)*cell)
	case rows < 1:
		return count((r.MaxTokens-3)*cell, cell)
	}
	scale := min(math.Floor(cols)*float64(cell)/float64(w), math.Floor(rows)*float64(cell)/float64(h))
	return count(int(float64(w)*scale/float64(r.PatchSize))*r.PatchSize, int(float64(h)*scale/float64(r.PatchSize))*r.PatchSize)
}
