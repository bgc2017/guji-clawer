package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"unicode"
	"unicode/utf8"
	"sort"
	"strings"
	"sync"
	"time"

	"jingdian/internal/glyph"
	"jingdian/internal/site"
)

type app struct {
	client *site.Client
	index  *glyph.Index
	libID  int
	mu     sync.Mutex // serialises font-set preparation per md5
	sets   map[string]*glyph.Set
}

func main() {
	var (
		addr    = flag.String("addr", "127.0.0.1:8088", "HTTP listen address")
		fontDir = flag.String("font", "fonts", "reference font file or directory (中华书局宋体)")
		cache   = flag.String("cache", "data", "cache directory for downloaded webfonts")
		libID   = flag.Int("lib", 6, "database id (the /6/ in the URL)")
		query   = flag.String("q", "", "run one search from the CLI and print JSON")
		num     = flag.Int("num", 50, "how many hits to return (max 50)")
		page    = flag.Int("page", 1, "first page, 50 hits per page")
		browser = flag.Bool("browser", false, "run searches inside a real Chrome tab (devtools protocol)")
	)
	flag.Parse()

	cl, err := site.New(*cache, *browser)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Fprintf(os.Stderr, "loading reference fonts from %s ...\n", *fontDir)
	idx, err := loadReference(*fontDir)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Fprintf(os.Stderr, "indexed %d glyphs\n", idx.Len())

	a := &app{client: cl, index: idx, libID: *libID, sets: map[string]*glyph.Set{}}

	if *query != "" {
		res, err := a.search(context.Background(), *query, *page, *num)
		if err != nil {
			log.Fatal(err)
	}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.SetEscapeHTML(false)
		_ = enc.Encode(res)
		return
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/search", a.handleSearch)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "glyphs": idx.Len()})
	})
	fmt.Fprintf(os.Stderr, "listening on http://%s/search?q=黄鹤楼\n", *addr)
	log.Fatal(http.ListenAndServe(*addr, mux))
}

// loadReference indexes one or more 中华书局宋体 files.
func loadReference(path string) (*glyph.Index, error) {
	ix := glyph.NewIndex()
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("reference font %q: %w (download 中华书局宋体00平面 first, see fonts/README.md)", path, err)
	}
	var files []string
	if info.IsDir() {
		entries, err := os.ReadDir(path)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			ext := strings.ToLower(filepath.Ext(e.Name()))
			if ext == ".ttf" || ext == ".otf" || ext == ".ttc" || ext == ".otc" {
				files = append(files, filepath.Join(path, e.Name()))
			}
		}
		sort.Strings(files)
	} else {
		files = []string{path}
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no .ttf/.otf font in %q", path)
	}
	for _, f := range files {
		n, err := glyph.AddFontFile(ix, f)
		if err != nil {
			fmt.Fprintf(os.Stderr, "skip %s: %v\n", f, err)
			continue
		}
		fmt.Fprintf(os.Stderr, "  %s: %d glyphs\n", filepath.Base(f), n)
	}
	if ix.Len() == 0 {
		return nil, fmt.Errorf("reference fonts produced an empty index")
	}
	return ix, nil
}

type item struct {
	ContentID   int64  `json:"contentId"`
	BookID      int64  `json:"bookId"`
	Title       string `json:"title"`
	Source      string `json:"source"`
	Catalogue   string `json:"catalogue"`
	Author      string `json:"author"`
	Category    string `json:"category"`
	Publisher   string `json:"publisher"`
	PubDate     string `json:"pubDate"`
	Snippet     string      `json:"snippet"`
	Highlights  []Highlight `json:"highlights,omitempty"`
	CoverURL    string `json:"coverUrl"`
	HasFullText bool   `json:"hasFullText"`
}

// Highlight marks where the matched term sits inside Snippet. Start/End are
// rune offsets into the plain text, so a client can rebuild the <em> markup the
// site renders without guessing.
type Highlight struct {
	Text  string `json:"text"`
	Start int    `json:"start"`
	End   int    `json:"end"`
}

type searchResult struct {
	Query     string `json:"query"`
	LibID     int    `json:"libId"`
	Page      int    `json:"page"`
	PageSize  int    `json:"pageSize"`
	Total     int    `json:"total"`
	Returned  int    `json:"returned"`
	ElapsedMS int64  `json:"elapsedMs"`
	Items     []item `json:"items"`
}

func (a *app) search(ctx context.Context, q string, page, num int) (*searchResult, error) {
	if num <= 0 {
		num = 50
	}
	if num > 50 {
		num = 50
	}
	start := time.Now()
	out := &searchResult{Query: q, LibID: a.libID, Page: page, PageSize: 50, Items: []item{}}

	got := 0
	for p := page; got < num; p++ {
		pageSize := num - got
		if pageSize > 50 {
			pageSize = 50
		}
		resp, err := a.client.Search(ctx, site.NewRequest(q, a.libID, p, pageSize))
		if err != nil {
			if got == 0 {
				return nil, err
		}
			break
		}
		if p == page {
			out.Total = resp.Data.Total
		}
		dec, err := a.decoder(ctx, resp.Fonts)
		if err != nil {
			return nil, err
		}
		if len(resp.Data.Rows) == 0 {
			break
		}
		for _, r := range resp.Data.Rows {
			it := item{
				ContentID:   r.ContentID,
				BookID:      r.BookID,
				Title:       r.Title,
				Source:      r.Source,
				Catalogue:   r.Catalogue,
				Author:      strings.TrimSpace(r.Author),
				Category:    r.Cat,
				Publisher:   r.Publisher,
				PubDate:     r.PubDate,
				CoverURL:    r.TitleImg,
				HasFullText: r.HasAuth,
			}
			if r.Description != "" && dec != nil {
				it.Snippet, it.Highlights = splitHighlight(dec.Text(r.Description))
			}
			out.Items = append(out.Items, it)
			got++
			if got >= num {
				break
			}
		}
		if len(resp.Data.Rows) < pageSize {
			break
	}
	}
	out.Returned = len(out.Items)
	out.ElapsedMS = time.Since(start).Milliseconds()
	return out, nil
}

// decoder downloads (and remembers) the fonts for one response bundle.
func (a *app) decoder(ctx context.Context, fonts map[string]site.FontInfo) (*glyph.Decoder, error) {
	all, ok := fonts["all"]
	if !ok || len(all.PathArray) == 0 {
		return nil, nil
	}
	key := all.MD5
	a.mu.Lock()
	defer a.mu.Unlock()
	if set, ok := a.sets[key]; ok {
		return glyph.NewDecoder(a.index, set), nil
	}

	set := &glyph.Set{}
	build := func(info site.FontInfo) (*glyph.Group, error) {
		g := &glyph.Group{MD5: info.MD5}
		for _, level := range info.PathArray {
			data, err := a.client.FontBytes(ctx, info, level)
			if err != nil {
				return nil, err
			}
			f, err := glyph.ParseObfFont(level, data)
			if err != nil {
				return nil, err
			}
			g.Fonts = append(g.Fonts, f)
		}
		return g, nil
	}
	g, err := build(all)
	if err != nil {
		return nil, err
	}
	set.All = g
	if ext, ok := fonts["ext"]; ok && len(ext.PathArray) > 0 {
		if g2, err := build(ext); err == nil {
			set.Ext = g2
		}
	}
	a.sets[key] = set
	return glyph.NewDecoder(a.index, set), nil
}

func (a *app) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing q"})
		return
	}
	page := atoi(r.URL.Query().Get("page"), 1)
	num := atoi(r.URL.Query().Get("num"), 50)
	if lib := atoi(r.URL.Query().Get("lib"), 0); lib > 0 {
		old := a.libID
		a.libID = lib
		defer func() { a.libID = old }()
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	res, err := a.search(ctx, q, page, num)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, res)
}

var (
	emOpen  = regexp.MustCompile(`<em[^>]*>`)
	emClose = regexp.MustCompile(`</em>`)
	tagRe   = regexp.MustCompile(`<[^>]*>`)
)

// splitHighlight turns the site's HTML snippet into plain text plus offsets for
// the highlighted term, keeping the markup information instead of throwing it
// away.
func splitHighlight(s string) (string, []Highlight) {
	type span struct{ start, end int }
	var b strings.Builder
	var spans []span
	openAt := -1
	runes := 0
	for i := 0; i < len(s); {
		switch {
		case emOpen.MatchString(s[i:]) && emOpen.FindStringIndex(s[i:])[0] == 0:
			openAt = runes
			i += emOpen.FindStringIndex(s[i:])[1]
		case emClose.MatchString(s[i:]) && emClose.FindStringIndex(s[i:])[0] == 0:
			if openAt >= 0 {
				spans = append(spans, span{openAt, runes})
				openAt = -1
			}
			i += emClose.FindStringIndex(s[i:])[1]
		case tagRe.MatchString(s[i:]) && tagRe.FindStringIndex(s[i:])[0] == 0:
			i += tagRe.FindStringIndex(s[i:])[1]
		default:
			r, size := utf8.DecodeRuneInString(s[i:])
			b.WriteRune(r)
			runes++
			i += size
		}
	}
	raw := b.String()
	lead := 0
	for _, r := range raw {
		if !unicode.IsSpace(r) {
			break
		}
		lead++
	}
	plain := strings.TrimSpace(raw)
	pr := []rune(plain)
	var marks []Highlight
	for _, sp := range spans {
		sp.start -= lead
		sp.end -= lead
		if sp.start < 0 {
			sp.start = 0
		}
		if sp.end > len(pr) {
			sp.end = len(pr)
		}
		if sp.start >= sp.end {
			continue
		}
		marks = append(marks, Highlight{Text: string(pr[sp.start:sp.end]), Start: sp.start, End: sp.end})
	}
	return plain, marks
}


func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func atoi(s string, def int) int {
	var v int
	if _, err := fmt.Sscanf(s, "%d", &v); err != nil {
		return def
	}
	return v
}
