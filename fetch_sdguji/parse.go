package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// 站点章节页是 SSR 页面，正文在 HTML 内联脚本里：
//   window._ROUTER_DATA = {"loaderData":{"__session/(lang$)/book/$":{...}}}
// 这里直接从浏览器拿到的原始 HTML 中截出 JSON，再解析，
// 不依赖页面 hydration 之后 window 上的数据（那份会被前端替换/清掉）。

type rawPara struct {
	ParagraphID        string `json:"paragraphId"`
	ParagraphType      int    `json:"paragraphType"`
	Content            string `json:"content"`
	ContentEncryptType int    `json:"contentEncryptType"`
	ChapterID          string `json:"chapterId"`
	StartPageID        string `json:"startPageId"`
}

type rawLoader struct {
	BookInfo      *BookInfo `json:"bookInfo"`
	ParagraphList []rawPara `json:"paragraphList"`
	SsrChapterID  string    `json:"ssrChapterId"`
}

// parseChapterHTML 从章节页 HTML 解析出整章正文与书目信息。
func parseChapterHTML(html []byte, chapterID string, wantCatalog bool) (*ChapterData, error) {
	obj, err := extractJSONAfter(html, "window._ROUTER_DATA = ")
	if err != nil {
		return nil, err
	}
	var root struct {
		LoaderData map[string]json.RawMessage `json:"loaderData"`
	}
	if err := json.Unmarshal(obj, &root); err != nil {
		return nil, fmt.Errorf("解析 _ROUTER_DATA 失败: %w", err)
	}
	if len(root.LoaderData) == 0 {
		return nil, fmt.Errorf("页面没有 loaderData")
	}

	for key, raw := range root.LoaderData {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			continue
		}
		if _, ok := fields["paragraphList"]; !ok {
			continue
		}
		var ld rawLoader
		if err := json.Unmarshal(raw, &ld); err != nil {
			return nil, fmt.Errorf("解析章节数据失败: %w", err)
		}

		cd := &ChapterData{
			Status:    "ok",
			Key:       key,
			ChapterID: ld.SsrChapterID,
			Paras:     make([]Para, 0, len(ld.ParagraphList)),
		}
		if cd.ChapterID == "" {
			cd.ChapterID = chapterID
		}
		if bi := ld.BookInfo; bi != nil {
			cd.BookID = bi.BookID
			cd.BookName = bi.BookName
			cd.Authors = bi.Authors
			cd.Dynasty = bi.DynastyCategoryName
			cd.TotalPage = bi.TotalPage
			if wantCatalog {
				cat := bi.Catalog
				cd.Catalog = &cat
			}
		}
		for _, p := range ld.ParagraphList {
			cd.Paras = append(cd.Paras, Para{
				ID:   p.ParagraphID,
				Type: p.ParagraphType,
				CID:  p.ChapterID,
				Page: p.StartPageID,
				Enc:  p.ContentEncryptType,
				Text: paragraphText(p.Content, p.ContentEncryptType),
			})
		}
		return cd, nil
	}
	return nil, fmt.Errorf("页面中未找到 paragraphList（可能被风控或章节不存在）")
}

// paragraphText 把 paragraph.content（JSON 字符串，含 lines）还原为文字。
// lineType: 1 正文；2 小字注（加括号）；3 空行/分隔。
func paragraphText(content string, encType int) string {
	if content == "" || encType != 0 {
		return ""
	}
	var inner struct {
		Lines []struct {
			Content  string `json:"content"`
			LineType int    `json:"lineType"`
		} `json:"lines"`
	}
	if err := json.Unmarshal([]byte(content), &inner); err == nil && len(inner.Lines) > 0 {
		var b strings.Builder
		for _, l := range inner.Lines {
			c := l.Content
			if c == "" {
				continue
			}
			switch l.LineType {
			case 3:
				continue
			case 2:
				b.WriteString("（")
				b.WriteString(c)
				b.WriteString("）")
			default:
				b.WriteString(c)
			}
		}
		return b.String()
	}
	// 少数情况 content 就是纯文本
	return content
}

// extractJSONAfter 从 HTML 中截取 marker 后的第一个完整 JSON 对象。
// 需要自己扫括号，因为 JSON 里含大量字符串（包括 } 和 \" 转义）。
func extractJSONAfter(html []byte, marker string) ([]byte, error) {
	i := bytes.Index(html, []byte(marker))
	if i < 0 {
		return nil, fmt.Errorf("页面里没有 %s（不是 SSR 页面或已被拦截）", marker)
	}
	start := i + len(marker)
	for start < len(html) {
		c := html[start]
		if c == ' ' || c == '\n' || c == '\r' || c == '\t' {
			start++
			continue
		}
		break
	}
	if start >= len(html) || html[start] != '{' {
		return nil, fmt.Errorf("marker 后不是 JSON 对象")
	}

	depth := 0
	inStr := false
	esc := false
	for j := start; j < len(html); j++ {
		c := html[j]
		if inStr {
			if esc {
				esc = false
				continue
			}
			switch c {
			case '\\':
				esc = true
			case '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return html[start : j+1], nil
			}
		}
	}
	return nil, fmt.Errorf("JSON 未闭合")
}
