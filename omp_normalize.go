package snapcompact

import (
	"regexp"
	"slices"
	"strings"
	"unicode"

	"golang.org/x/image/font/sfnt"
	"golang.org/x/text/unicode/norm"
)

var ompANSI = regexp.MustCompile("\\x1b(?:\\[[0-?]*[ -/]*[@-~]|\\][^\\x07\\x1b]*(?:\\x07|\\x1b\\\\)|[PX^_][^\\x1b]*\\x1b\\\\|[ -/]*[0-~])")
var ompSpaces = regexp.MustCompile(" +")

func ompWhitespace(r rune) bool {
	return r != 0x85 && unicode.IsSpace(r) || r == 0xfeff
}

func ompCollapse(text string) string {
	if strings.ContainsRune(text, '\x1b') {
		text = ompANSI.ReplaceAllString(text, "")
	}
	var b strings.Builder
	space, line := false, false
	for _, r := range text {
		if ompWhitespace(r) || unicode.Is(unicode.Cf, r) {
			space = space || !unicode.Is(unicode.Cf, r)
			line = line || r == '\n' || r == '\r' || r == '\u2028' || r == '\u2029'
			continue
		}
		if b.Len() > 0 {
			if line {
				b.WriteRune(ompNewline)
			} else if space {
				b.WriteByte(' ')
			}
		}
		space, line = false, false
		b.WriteRune(r)
	}
	return strings.Trim(b.String(), " █")
}

func ompLatin(r rune) bool {
	return r >= 0x20 && r < 0x7f || r >= 0xa0 && r <= 0xff
}

func ompPictograph(r rune) bool {
	i, _ := slices.BinarySearchFunc(ompPictographs[:], r, func(span [2]rune, r rune) int {
		switch {
		case span[1] < r:
			return -1
		case span[0] > r:
			return 1
		default:
			return 0
		}
	})
	return i < len(ompPictographs) && ompPictographs[i][0] <= r
}

func ompFoldASCII(r rune) (string, bool) {
	text := string(r)
	decomposed := norm.NFKD.String(text)
	var b strings.Builder
	for _, ch := range decomposed {
		if !unicode.Is(unicode.M, ch) {
			b.WriteRune(ch)
		}
	}
	if decomposed = b.String(); decomposed == text {
		return "", false
	}
	b.Reset()
	for _, ch := range decomposed {
		if ompLatin(ch) {
			b.WriteRune(ch)
		} else if s, ok := ompSymbolFold[ch]; ok {
			b.WriteString(s)
		} else {
			return "", false
		}
	}
	return b.String(), true
}

type ompScan struct {
	text               string
	graphics, fallback int
}

func (s ompScan) safe() bool { return s.fallback*20 <= s.graphics }

func ompNormalize(text string, font string) ompScan {
	var out ompScan
	f := ompBitmapFonts()[font]
	var b strings.Builder
	var buffer sfnt.Buffer
	for _, r := range ompCollapse(text) {
		if r == ompDimOn || r == ompDimOff || r == ompNewline {
			b.WriteRune(r)
			continue
		}
		emoji := ompPictograph(r)
		switch {
		case ompLatin(r):
			b.WriteRune(r)
		case ompSymbolFold[r] != "":
			b.WriteString(ompSymbolFold[r])
		case r >= 0x2500 && r <= 0x257f:
			switch r {
			case '│', '┃':
				b.WriteByte('|')
			case '─', '━':
				b.WriteByte('-')
			default:
				b.WriteByte('+')
			}
		default:
			if folded, ok := ompFoldASCII(r); ok {
				b.WriteString(folded)
			} else if !emoji && ompSupports(f, &buffer, r) {
				b.WriteRune(r)
			} else if emoji || unicode.IsOneOf([]*unicode.RangeTable{unicode.Cc, unicode.Mn, unicode.Me, unicode.Cs}, r) {
				continue
			} else {
				b.WriteByte('?')
				out.fallback++
			}
		}
		out.graphics++
	}
	out.text = strings.Trim(ompSpaces.ReplaceAllString(b.String(), " "), " █")
	return out
}

// NormalizeOMP converts a serialized transcript to the reference frame alphabet.
func NormalizeOMP(text string, shape OMPShape) string {
	return ompNormalize(text, shape.Font).text
}

func ompCJKHeavy(text string) bool {
	wide, graphics := 0, 0
	for _, r := range ompCollapse(text) {
		if r == ' ' || r == ompNewline || unicode.IsOneOf([]*unicode.RangeTable{unicode.Cc, unicode.Mn, unicode.Me, unicode.Cs}, r) {
			continue
		}
		graphics++
		if ompWide(r) {
			wide++
		}
	}
	return wide >= 8 && wide*4 >= graphics
}

var ompAlphabetic = regexp.MustCompile(`[A-Za-zÀ-ÖØ-öø-ÿ]+`)
var ompStopwordSet = func() map[string]bool {
	out := make(map[string]bool)
	for word := range strings.FieldsSeq(ompStopwords) {
		out[word] = true
	}
	return out
}()

func ompDimWords(text string) string {
	var b strings.Builder
	dim := false
	start := 0
	write := func(part string) {
		if !dim {
			part = ompAlphabetic.ReplaceAllStringFunc(part, func(word string) string {
				if ompStopwordSet[strings.ToLower(word)] {
					return string(ompDimOn) + word + string(ompDimOff)
				}
				return word
			})
		}
		b.WriteString(part)
	}
	for i, r := range text {
		if r == ompDimOn || r == ompDimOff {
			write(text[start:i])
			b.WriteRune(r)
			dim = r == ompDimOn
			start = i + 1
		}
	}
	write(text[start:])
	return b.String()
}
