// Package glyph turns the site's obfuscated webfonts back into readable text.
//
// The site keeps the shape of every glyph but shuffles the codepoints, so a
// search snippet arrives as private-use characters. The shapes themselves are
// taken from the publicly distributed "中华书局宋体" families, which means the
// mapping can be recovered by comparing outlines: for each obfuscated glyph we
// look for a reference glyph with the same point count, the same bounding box
// and a near-identical point sequence.
package glyph

import (
	"os"
	"math"
	"sync"

	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

// Point is one outline control point in a 1000-unit em space.
type Point struct {
	X, Y int16
}

// Outline is a glyph's flattened outline plus a few cheap features.
type Outline struct {
	Pts   []Point
	MinX  int16
	MinY  int16
	MaxX  int16
	MaxY  int16
	Runes []rune // candidate characters sharing this exact shape
}

// Descriptor is the minimal feature set used for the first-pass lookup.
func (o *Outline) Descriptor() (count int, minX, minY, maxX, maxY int16) {
	return len(o.Pts), o.MinX, o.MinY, o.MaxX, o.MaxY
}

const em = 1000.0

// outlineOf extracts an outline from an sfnt font by glyph id.
func outlineOf(f *sfnt.Font, gid sfnt.GlyphIndex, buf *sfnt.Buffer) (*Outline, error) {
	if gid == 0 {
		return nil, nil
	}
	upem := float64(f.UnitsPerEm())
	segs, err := f.LoadGlyph(buf, gid, fixedPPEM(upem), nil)
	if err != nil {
		return nil, err
	}
	n := 0
	for _, s := range segs {
		n += argCount(s.Op)
	}
	if n == 0 {
		return &Outline{}, nil
	}
	pts := make([]Point, 0, n)
	for _, s := range segs {
		for i := 0; i < argCount(s.Op); i++ {
			// sfnt returns fixed-point units already scaled by ppem/upem.
			pts = append(pts, Point{
				X: int16(math.Round(float64(s.Args[i].X) / 64.0)),
				Y: int16(math.Round(float64(s.Args[i].Y) / 64.0)),
			})
		}
	}
	return newOutline(pts), nil
}

func newOutline(pts []Point) *Outline {
	o := &Outline{Pts: pts, MinX: pts[0].X, MaxX: pts[0].X, MinY: pts[0].Y, MaxY: pts[0].Y}
	for _, p := range pts {
		if p.X < o.MinX {
			o.MinX = p.X
		}
		if p.X > o.MaxX {
			o.MaxX = p.X
		}
		if p.Y < o.MinY {
			o.MinY = p.Y
		}
		if p.Y > o.MaxY {
			o.MaxY = p.Y
		}
	}
	return o
}

// Index is the outline database built from reference fonts.
type Index struct {
	byCount map[int][]*Outline
	mu      sync.Mutex
	glyphMu sync.Mutex
	bufs    map[*sfnt.Font]*sfnt.Buffer
}

// NewIndex returns an empty index.
func NewIndex() *Index {
	return &Index{byCount: map[int][]*Outline{}, bufs: map[*sfnt.Font]*sfnt.Buffer{}}
}

// AddFont indexes every mapped character of one reference font.
func (ix *Index) AddFont(f *sfnt.Font, buf *sfnt.Buffer) (int, error) {
	added := 0
	// Only wide ranges that can appear in Chinese text; scanning the whole
	// Unicode space would be pointless and slow.
	ranges := [][2]rune{
		{0x20, 0x7F}, {0xA0, 0x100}, {0x2000, 0x2070}, {0x2460, 0x2500},
		{0x2E80, 0x3000}, {0x3000, 0x3040}, {0x3400, 0x4E00}, {0x4E00, 0xA000}, {0xF900, 0xFB00},
		{0xFE10, 0xFE20}, {0xFF00, 0xFFF0}, {0x20000, 0x2FA20},
	}
	seen := make(map[rune]bool)
	for _, r := range ranges {
		for cp := r[0]; cp < r[1]; cp++ {
			gid, err := f.GlyphIndex(buf, cp)
			if err != nil || gid == 0 || seen[cp] {
				continue
			}
			seen[cp] = true
			o, err := outlineOf(f, gid, buf)
			if err != nil || o == nil || len(o.Pts) == 0 {
				continue
			}
			o.Runes = []rune{cp}
			ix.byCount[len(o.Pts)] = append(ix.byCount[len(o.Pts)], o)
			added++
		}
	}
	return added, nil
}

// Merge combines two indexes.
func (ix *Index) Merge(other *Index) {
	for k, v := range other.byCount {
		ix.byCount[k] = append(ix.byCount[k], v...)
	}
}

// Len reports how many shaped characters the index holds.
func (ix *Index) Len() int {
	n := 0
	for _, v := range ix.byCount {
		n += len(v)
	}
	return n
}

const (
	bboxTolerance = 32.0 // font versions differ by a hair, ~1.5% of an em
	maxDistance   = 36.0 // mean |dx|+|dy| per point
	tieWindow     = 3.0
)

// Result is the decoded character for one glyph.
type Result struct {
	Rune  rune
	Dist  float64
	Tied  bool // several characters share an identical outline
	Empty bool
}

// Match finds the reference character closest to the given outline.
func (ix *Index) Match(o *Outline) Result {
	if o == nil || len(o.Pts) == 0 {
		return Result{Empty: true}
	}
	cands := ix.byCount[len(o.Pts)]
	best := Result{Rune: 0, Dist: math.MaxFloat64}
	var tied []rune

	for _, c := range cands {
		if absf(float64(c.MinX-o.MinX)) > bboxTolerance ||
			absf(float64(c.MinY-o.MinY)) > bboxTolerance ||
			absf(float64(c.MaxX-o.MaxX)) > bboxTolerance ||
			absf(float64(c.MaxY-o.MaxY)) > bboxTolerance {
			continue
		}
		s := distance(o.Pts, c.Pts)
		if s > maxDistance {
			continue
		}
		if s < best.Dist-tieWindow {
			best = Result{Rune: pickRune(c.Runes), Dist: s, Tied: len(c.Runes) > 1}
			tied = append(tied[:0], c.Runes...)
		} else if s <= best.Dist+tieWindow {
			if best.Rune == 0 || s < best.Dist {
				best.Dist = math.Min(best.Dist, s)
			}
			tied = append(tied, c.Runes...)
		}
	}
	if best.Rune == 0 {
		return Result{}
	}
	if len(tied) > 0 {
		best.Rune = pickRune(tied)
		best.Tied = true
	}
	return best
}

func absf(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

func distance(a, b []Point) float64 {
	if len(a) != len(b) {
		return math.MaxFloat64
	}
	var sum float64
	for i := range a {
		sum += absf(float64(a[i].X - b[i].X))
		sum += absf(float64(a[i].Y - b[i].Y))
	}
	return sum / float64(len(a))
}

// pickRune resolves same-shaped characters (a radical and its ideograph, a
// full-width comma and its presentation form, ...) by preferring the codepoint
// a reader would expect to see.
func pickRune(rs []rune) rune {
	if len(rs) == 0 {
		return 0
	}
	best := rs[0]
	bestRank := runeRank(best)
	for _, r := range rs[1:] {
		if rk := runeRank(r); rk < bestRank {
			best, bestRank = r, rk
		}
	}
	return best
}

func runeRank(r rune) int {
	switch {
	case r >= 0x4E00 && r <= 0x9FFF:
		return 0 // unified ideographs
	case r >= 0x20 && r <= 0x7E:
		return 0 // ascii
	case r >= 0x3000 && r <= 0x303F:
		return 0 // cjk punctuation
	case r >= 0x3400 && r <= 0x4DBF:
		return 1 // extension A
	case r >= 0xFF00 && r <= 0xFFEF:
		return 1 // full-width forms
	case r >= 0x20000:
		return 2 // extensions B+
	case r >= 0x2E80 && r <= 0x2FFF:
		return 3 // radicals, strokes
	case r >= 0xFE10 && r <= 0xFE1F:
		return 4 // presentation forms for vertical text
	case r >= 0xF900 && r <= 0xFAFF:
		return 4 // compatibility ideographs
	default:
		return 2
	}
}

func argCount(op sfnt.SegmentOp) int {
	switch op {
	case sfnt.SegmentOpMoveTo, sfnt.SegmentOpLineTo:
		return 1
	case sfnt.SegmentOpQuadTo:
		return 2
	case sfnt.SegmentOpCubeTo:
		return 3
	default:
		return 0
	}
}

func fixedPPEM(upem float64) fixed.Int26_6 { return fixed.Int26_6(upem * 64) }


// AddFontFile indexes one reference font file. Collections (.ttc/.otc) are
// expanded face by face.
func AddFontFile(ix *Index, path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	total := 0
	if coll, err := sfnt.ParseCollection(data); err == nil && coll.NumFonts() > 0 {
		for i := 0; i < coll.NumFonts(); i++ {
			f, err := coll.Font(i)
			if err != nil {
				continue
			}
			n, _ := ix.AddFont(f, &sfnt.Buffer{})
			total += n
		}
		return total, nil
	}
	f, err := sfnt.Parse(data)
	if err != nil {
		return 0, err
	}
	return ix.AddFont(f, &sfnt.Buffer{})
}

// Level is one candidate character with its distance, for diagnostics.
type Level struct {
	Rune rune
	Dist float64
	BBox float64
}

// Explore lists the closest reference characters regardless of threshold.
func (ix *Index) Explore(o *Outline, limit int) []Level {
	if o == nil || len(o.Pts) == 0 {
		return nil
	}
	var out []Level
	for _, c := range ix.byCount[len(o.Pts)] {
		d := distance(o.Pts, c.Pts)
		bb := math.Max(math.Max(absf(float64(c.MinX-o.MinX)), absf(float64(c.MaxX-o.MaxX))),
			math.Max(absf(float64(c.MinY-o.MinY)), absf(float64(c.MaxY-o.MaxY))))
		out = append(out, Level{Rune: pickRune(c.Runes), Dist: d, BBox: bb})
	}
	sortLevels(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func sortLevels(l []Level) {
	for i := 1; i < len(l); i++ {
		for j := i; j > 0 && l[j].Dist < l[j-1].Dist; j-- {
			l[j], l[j-1] = l[j-1], l[j]
		}
	}
}
