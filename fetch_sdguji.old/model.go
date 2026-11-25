package main

import "strings"

// ---------- 站点数据结构（来自章节页 _ROUTER_DATA 的 loaderData） ----------

type Author struct {
	PersName           string `json:"persName"`
	ResponsibleTypeStr string `json:"responsibleTypeStr"`
	DynastyName        string `json:"dynastyName"`
}

type ChapterName struct {
	Content string `json:"content"`
}

// Chapter 目录中的一个章（卷）。站点支持 subChapters 二级目录。
type Chapter struct {
	ChapterID                   string        `json:"chapterId"`
	ChapterName                 []ChapterName `json:"chapterName"`
	ChapterType                 int           `json:"chapterType"`
	ChapterLevel                int           `json:"chapterLevel"`
	ParentChapterID             string        `json:"parentChapterId"`
	StartPageNum                int           `json:"startPageNum"`
	EndPageNumWithoutSubchapter int           `json:"endPageNumWithoutSubchapter"`
	ParagraphCount              int           `json:"paragraphCount"`
	VolumeID                    string        `json:"volumeId"`
	SubChapters                 []*Chapter    `json:"subChapters"`
}

type Catalog struct {
	Chapters []*Chapter `json:"chapters"`
}

type BookInfo struct {
	BookID              string   `json:"bookId"`
	BookName            string   `json:"bookName"`
	Authors             []Author `json:"authors"`
	DynastyCategoryName string   `json:"dynastyCategoryName"`
	TotalPage           int      `json:"totalPage"`
	Catalog             Catalog  `json:"catalog"`
}

type Edition struct {
	Edition            string `json:"edition"`
	EditionDynastyName string `json:"editionDynastyName"`
}

// Para 是章节页 SSR 里 paragraphList 解析后的一段文字。
// Type 为 1 表示正文；其他类型可能是图像/表等，text 仍尽量保留。
type Para struct {
	ID   string `json:"id"`
	Type int    `json:"type"`
	CID  string `json:"cid"`
	Page string `json:"page"`
	Enc  int    `json:"enc"`
	Text string `json:"text"`
}

// ChapterData 是从页面 window._ROUTER_DATA 中取到的整章数据。
type ChapterData struct {
	Status    string   `json:"status"`
	Key       string   `json:"key"`
	ChapterID string   `json:"chapterId"`
	BookID    string   `json:"bookId"`
	BookName  string   `json:"bookName"`
	Authors   []Author `json:"authors"`
	Dynasty   string   `json:"dynasty"`
	TotalPage int      `json:"totalPage"`
	Catalog   *Catalog `json:"catalog"`
	Paras     []Para   `json:"paras"`
	DiagText  string   `json:"-"`
}

func (c *Chapter) Title() string {
	var b strings.Builder
	for _, n := range c.ChapterName {
		b.WriteString(strings.TrimSpace(n.Content))
	}
	t := strings.TrimSpace(b.String())
	if t == "" {
		t = "未命名章节"
	}
	return t
}

func (c *Chapter) PageRange() (int, int) {
	start := c.StartPageNum
	end := c.EndPageNumWithoutSubchapter
	if end <= 0 {
		end = start
	}
	return start, end
}

func (c *Chapter) hasSubs() bool { return len(c.SubChapters) > 0 }

// flattenChapters 按阅读顺序展开目录（父章在前，子章紧随）。
func flattenChapters(chs []*Chapter) []*Chapter {
	var out []*Chapter
	var walk func([]*Chapter)
	walk = func(list []*Chapter) {
		for _, c := range list {
			if c == nil {
				continue
			}
			out = append(out, c)
			walk(c.SubChapters)
		}
	}
	walk(chs)
	return out
}

// countChapters 统计目录中的实际章数（含子章）。
func countChapters(chs []*Chapter) int { return len(flattenChapters(chs)) }

func authorLine(bi *BookInfo) string {
	var parts []string
	dyn := strings.TrimSpace(bi.DynastyCategoryName)
	for _, a := range bi.Authors {
		name := strings.TrimSpace(a.PersName)
		if name == "" {
			continue
		}
		p := name
		if r := strings.TrimSpace(a.ResponsibleTypeStr); r != "" {
			p += " " + r
		}
		parts = append(parts, p)
	}
	s := strings.Join(parts, " · ")
	if dyn != "" {
		if s == "" {
			return "[" + dyn + "]"
		}
		return "[" + dyn + "] " + s
	}
	return s
}
