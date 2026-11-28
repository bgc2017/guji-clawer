// Package site talks to jingdian.ancientbooks.cn.
package site

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const baseURL = "https://jingdian.ancientbooks.cn"

// Row is one search hit. Only the fields the tool exposes are decoded.
type Row struct {
	ContentID   int64  `json:"contentId"`
	BookID      int64  `json:"bookId"`
	Title       string `json:"title"`
	Source      string `json:"source"`
	Catalogue   string `json:"catalogue"`
	Author      string `json:"author"`
	Author2     string `json:"author2"`
	Cat         string `json:"cat"`
	ShortTitle  string `json:"shortTitle"`
	Publisher   string `json:"publisher"`
	PubDate     string `json:"pubdate"`
	Description string `json:"description"`
	TitleImg    string `json:"titleImg"`
	HasAuth     bool   `json:"hasAuth"`
	HasTip      bool   `json:"hasTip"`
	AllMD5      string `json:"allMd5"`
	ExtMD5      string `json:"extMd5"`
}

// FontInfo describes one generated font bundle.
type FontInfo struct {
	Folder    string   `json:"folder"`
	MD5       string   `json:"md5"`
	Path      string   `json:"path"`
	PathArray []string `json:"pathArray"`
}

// Response is the /v1/search envelope.
type Response struct {
	Success bool   `json:"success"`
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		Total      int    `json:"total"`
		Rows       []Row  `json:"rows"`
		PageNo     int    `json:"pageNo"`
		PageNum    int    `json:"pageNum"`
		PageSize   int    `json:"pageSize"`
		SearchWord string `json:"searchWord"`
	} `json:"data"`
	Fonts map[string]FontInfo `json:"fonts"`
}

// SearchRequest is the payload the web app posts.
type SearchRequest struct {
	LibID          int    `json:"libId"`
	ModelID        int    `json:"modelId"`
	CatID          any    `json:"catId"`
	RootID         any    `json:"rootId"`
	SortMethod     int    `json:"sortMethod"`
	IsAdvance      bool   `json:"isAdvance"`
	IsWordDistance bool   `json:"isWordDistance"`
	Distance       bool   `json:"distance"`
	UseSynonyms    bool   `json:"useSynonyms"`
	UseProperNouns bool   `json:"useProperNouns"`
	ConditionJSON  string `json:"conditionJson"`
	MyBookSelf     bool   `json:"myBookSelf"`
	CrossingZhu    bool   `json:"crossingZhu"`
	IgnorePunc     bool   `json:"ignorePunc"`
	OrderByColumn  string `json:"orderByColumn"`
	HasBuy         bool   `json:"hasBuy"`
	IsAsc          string `json:"isAsc"`
	NeedRecord     bool   `json:"needRecord"`
	PageNum        int    `json:"pageNum"`
	PageSize       int    `json:"pageSize"`
}

// Condition is one row of the advanced-search condition list. The web app sends
// the keyword here rather than in a flat txt field; leaving it out makes the
// server fall back to "no condition" and return the whole library.
type Condition struct {
	Column     string `json:"column"`
	SearchTxt  string `json:"searchTxt"`
	Relation   string `json:"relation"`
}

// NewRequest builds the canonical full-text request.
func NewRequest(q string, libID, page, pageSize int) SearchRequest {
	cond, _ := json.Marshal([]Condition{{Column: "txt", SearchTxt: q, Relation: "and"}})
	return SearchRequest{
		LibID:         libID,
		ModelID:       5, // modelId=5 is 全文 (2 is 书目)
		CatID:         nil,
		SortMethod:    1,
		ConditionJSON: string(cond),
		IgnorePunc:    true,
		OrderByColumn: "cat",
		IsAsc:         "asc",
		NeedRecord:    true,
		PageNum:       page,
		PageSize:      pageSize,
	}
}

// Client is a small stateful HTTP client (cookies must survive between calls).
type Client struct {
	http     *http.Client
	FontDir  string
	browser  bool
	mu       sync.Mutex
	fontHTTP *http.Client
}

// New returns a client. If useBrowser is true the search call is executed in a
// real Chrome tab through the devtools protocol.
func New(fontDir string, useBrowser bool) (*Client, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	c := &http.Client{
		Jar:     jar,
		Timeout: 60 * time.Second,
	}
	if fontDir != "" {
		if err := os.MkdirAll(fontDir, 0o755); err != nil {
			return nil, err
	}
	}
	return &Client{
		http:     c,
		fontHTTP: &http.Client{Timeout: 60 * time.Second},
		FontDir:  fontDir,
		browser:  useBrowser,
	}, nil
}

func (c *Client) do(ctx context.Context, method, path string, body any) ([]byte, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, baseURL+path, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("siteid", "1")
	req.Header.Set("Origin", baseURL)
	req.Header.Set("Referer", baseURL+"/6/search")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0 Safari/537.36")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, truncate(string(data), 200))
	}
	return data, nil
}

// Search runs one page of full-text search.
func (c *Client) Search(ctx context.Context, req SearchRequest) (*Response, error) {
	var raw []byte
	var err error
	if c.browser {
		raw, err = c.searchViaBrowser(ctx, req)
	} else {
		raw, err = c.do(ctx, http.MethodPost, "/v1/search", req)
	}
	if err != nil {
		return nil, err
	}
	var out Response
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if out.Code != 200 || !out.Success {
		return nil, fmt.Errorf("api code %d: %s", out.Code, out.Message)
	}
	return &out, nil
}

// FontBytes downloads one level font of a bundle, caching it on disk by md5.
func (c *Client) FontBytes(ctx context.Context, info FontInfo, level string) ([]byte, error) {
	name := fmt.Sprintf("%s_%s.woff", info.MD5, level)
	if c.FontDir != "" {
		p := filepath.Join(c.FontDir, name)
		if b, err := os.ReadFile(p); err == nil && len(b) > 0 {
			return b, nil
		}
	}
	url := fmt.Sprintf("%s/font/%s/%s/%s.woff", baseURL, info.Folder, info.MD5, level)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Referer", baseURL+"/6/search")
	resp, err := c.fontHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("font %s: http %d", level, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if c.FontDir != "" {
		_ = os.WriteFile(filepath.Join(c.FontDir, name), body, 0o644)
	}
	return body, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
