package glyph

import (
	"fmt"
	"strings"
	"sync"

	"golang.org/x/image/font/sfnt"

	"jingdian/internal/woff"
)

// ObfFont is one downloaded obfuscated webfont together with a per-codepoint
// outline cache.
type ObfFont struct {
	Name string // level id, used for debugging
	font *sfnt.Font
	buf  *sfnt.Buffer
	mu   sync.Mutex
	seen map[rune]*Outline
	hit  map[rune]bool
}

// ParseObfFont parses raw webfont bytes (WOFF 1.0 or bare sfnt).
func ParseObfFont(name string, data []byte) (*ObfFont, error) {
	sfntData := data
	if woff.IsWOFF2(data) {
		return nil, fmt.Errorf("font %s: WOFF2 is not supported, only WOFF1", name)
	}
	if woff.IsWOFF(data) {
		var err error
		sfntData, err = woff.ToSFNT(data)
		if err != nil {
			return nil, fmt.Errorf("font %s: %w", name, err)
		}
	}
	f, err := sfnt.Parse(sfntData)
	if err != nil {
		return nil, fmt.Errorf("font %s: %w", name, err)
	}
	return &ObfFont{
		Name: name,
		font: f,
		buf:  &sfnt.Buffer{},
		seen: map[rune]*Outline{},
		hit:  map[rune]bool{},
	}, nil
}

// Has reports whether the font maps cp to a real glyph.
func (o *ObfFont) Has(cp rune) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if v, ok := o.hit[cp]; ok {
		return v
	}
	gid, err := o.font.GlyphIndex(o.buf, cp)
	v := err == nil && gid != 0
	o.hit[cp] = v
	return v
}

// Outline returns the shape mapped to cp.
func (o *ObfFont) Outline(cp rune) *Outline {
	o.mu.Lock()
	defer o.mu.Unlock()
	if out, ok := o.seen[cp]; ok {
		return out
	}
	gid, err := o.font.GlyphIndex(o.buf, cp)
	if err != nil || gid == 0 {
		o.seen[cp] = nil
		return nil
	}
	out, err := outlineOf(o.font, gid, o.buf)
	if err != nil {
		out = nil
	}
	o.seen[cp] = out
	return out
}

// Group is one CSS font stack (the "all" or "ext" set). Rendering resolves a
// codepoint against the first font in the stack that defines it, so order
// matters here.
type Group struct {
	MD5   string
	Fonts []*ObfFont
}

// Pick resolves cp to the font that actually renders it.
//
// The generated bundles pad their early levels with placeholder mappings to
// blank glyphs, so a codepoint can be "present" in a font that draws nothing;
// those entries are skipped and the search continues down the stack.
func (g *Group) Pick(cp rune) (*ObfFont, *Outline) {
	for _, f := range g.Fonts {
		if !f.Has(cp) {
			continue
		}
		if out := f.Outline(cp); out != nil && len(out.Pts) > 0 {
			return f, out
		}
	}
	return nil, nil
}

// Set is a search response's fonts field.
type Set struct {
	All *Group
	Ext *Group
}

// Decoder decodes obfuscated text with a reference index.
type Decoder struct {
	Index *Index
	Set   *Set
	cache map[cacheKey]rune
	mu    sync.Mutex
}

type cacheKey struct {
	cp rune
}

// dropMark is stored in the cache for codepoints that draw nothing at all; the
// site sprinkles a few of these into snippets as anti-copy filler.
const dropMark = -1

// NewDecoder wires a font set to a reference index.
func NewDecoder(idx *Index, set *Set) *Decoder {
	return &Decoder{Index: idx, Set: set, cache: map[cacheKey]rune{}}
}

func (d *Decoder) decodeRune(cp rune) (rune, bool) {
	if d.Set == nil {
		return cp, false
	}
	key := cacheKey{cp: cp}
	d.mu.Lock()
	r, ok := d.cache[key]
	d.mu.Unlock()
	if ok {
		return r, r == dropMark
	}

	var groups []*Group
	if d.Set.All != nil {
		groups = append(groups, d.Set.All)
	}
	if d.Set.Ext != nil {
		groups = append(groups, d.Set.Ext)
	}
	// A codepoint can be drawn by several levels of the same stack (each level
	// holds a different slice of the character set), and the levels that only
	// carry placeholder mappings draw nothing. So try every level and keep the
	// reference character that matches most closely.
	bestRune, bestDist := rune(0), 1e9
	drawn := false
	for _, g := range groups {
		for _, f := range g.Fonts {
			out := f.Outline(cp)
			if out == nil || len(out.Pts) == 0 {
				continue
			}
			drawn = true
			if res := d.Index.Match(out); res.Rune != 0 && res.Dist < bestDist {
				bestRune, bestDist = res.Rune, res.Dist
			}
		}
	}
	d.mu.Lock()
	if bestRune == 0 && drawn {
		d.cache[key] = cp // rare char the reference font lacks: keep it verbatim
	} else if bestRune == 0 {
		d.cache[key] = dropMark // nothing is drawn at all: invisible filler
	} else {
		d.cache[key] = bestRune
	}
	r = d.cache[key]
	d.mu.Unlock()
	return r, r == dropMark
}

// Text decodes one string; characters that cannot be resolved (rare glyphs
// from the 02/15/16 planes) are kept as they are.
func (d *Decoder) Text(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, cp := range s {
		// Obfuscation only ever replaces ideographs with codepoints up in the
		// supplementary planes; ASCII (including the spaces inside the <em>
		// tags) and ordinary punctuation arrive as themselves.
		if cp < 0x30000 {
			b.WriteRune(cp)
			continue
		}
		r, drop := d.decodeRune(cp)
		if drop {
			continue
		}
		if r != 0 {
			b.WriteRune(normalizeRune(r))
			continue
		}
		b.WriteRune(cp)
	}
	return b.String()
}

// normalizeRune converts the vertical/presentation forms the site substitutes
// back into ordinary punctuation.
var presentation = map[rune]rune{
	'﹁': '“', '﹂': '”', '﹃': '‘', '﹄': '’',
	'﹐': '，', '﹑': '、', '﹒': '。', '﹔': '；',
	'﹕': '：', '﹝': '【', '﹞': '】', '﹗': '！',
	'﹖': '？', '﹙': '（', '﹚': '）', '﹛': '｛', '﹜': '｝',
}

func normalizeRune(r rune) rune {
	if v, ok := presentation[r]; ok {
		return v
	}
	return r
}
