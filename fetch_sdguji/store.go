package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

// ---------- 章节记录与清单 ----------

type ChapterRecord struct {
	Index      int    `json:"index"`
	ID         string `json:"chapter_id"`
	Title      string `json:"title"`
	StartPage  int    `json:"start_page"`
	EndPage    int    `json:"end_page"`
	Expected   int    `json:"expected_paragraphs"`
	Paragraphs int    `json:"paragraphs"`
	Chars      int    `json:"chars"`
	File       string `json:"file"`
	Status     string `json:"status"` // done / cached / failed / empty
	Note       string `json:"note,omitempty"`
	ElapsedMS  int64  `json:"elapsed_ms"`
}

type Manifest struct {
	BookID    string          `json:"book_id"`
	BookName  string          `json:"book_name"`
	Author    string          `json:"author"`
	Dynasty   string          `json:"dynasty"`
	SourceURL string          `json:"source_url"`
	TotalPage int             `json:"total_page"`
	Chapters  []ChapterRecord `json:"chapters"`
	CrawledAt string          `json:"crawled_at"`
	Version   string          `json:"version"`
}

func safeName(s string) string {
	s = strings.TrimSpace(s)
	repl := strings.NewReplacer(
		"/", "_", "\\", "_", ":", "_", "：", "_", "*", "_",
		"?", "_", `"`, "_", "<", "_", ">", "_", "|", "_",
		"\n", "", "\r", "", "\t", "", " ", "",
	)
	s = repl.Replace(s)
	if s == "" {
		s = "untitled"
	}
	if utf8.RuneCountInString(s) > 60 {
		s = string([]rune(s)[:60])
	}
	return s
}

func saveJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (r *ChapterRecord) header() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", r.Title)
	if r.EndPage > r.StartPage {
		fmt.Fprintf(&b, "（第%d–%d页）\n", r.StartPage, r.EndPage)
	} else if r.StartPage > 0 {
		fmt.Fprintf(&b, "（第%d页）\n", r.StartPage)
	}
	b.WriteString("\n")
	return b.String()
}

func writeChapterFile(path string, rec *ChapterRecord, paras []string) (int, error) {
	var b strings.Builder
	b.WriteString(rec.header())
	chars, n := 0, 0
	for _, p := range paras {
		t := strings.TrimSpace(p)
		if t == "" {
			continue
		}
		b.WriteString(t)
		b.WriteString("\n")
		chars += utf8.RuneCountInString(t)
		n++
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return 0, err
	}
	rec.Chars = chars
	rec.Paragraphs = n
	return chars, nil
}

func countFileChars(path string) (int, int) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, 0
	}
	text := string(data)
	lines := 0
	for _, l := range strings.Split(text, "\n") {
		if strings.TrimSpace(l) != "" {
			lines++
		}
	}
	return utf8.RuneCountInString(text), lines
}

// writeCombined 把各章拼成全本。
func writeCombined(dir, bookName, author string, m *Manifest) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "《%s》\n", bookName)
	if author != "" {
		fmt.Fprintf(&b, "%s\n", author)
	}
	fmt.Fprintf(&b, "共%d章 / %d页\n", len(m.Chapters), m.TotalPage)

	for _, rec := range m.Chapters {
		var body string
		switch rec.Status {
		case "failed":
			body = "（本章未能取得正文）\n"
		case "empty":
			body = "（本章无文本）\n"
		default:
			data, err := os.ReadFile(filepath.Join(dir, rec.File))
			if err != nil {
				// 本次未抓取且本地也没有，跳过
				continue
			}
			body = string(data)
			if i := strings.Index(body, "\n\n"); i >= 0 {
				body = body[i+2:]
			}
			body = strings.TrimRight(body, "\n") + "\n"
		}

		b.WriteString("\n")
		fmt.Fprintf(&b, "==================== [%03d] %s", rec.Index, rec.Title)
		if rec.EndPage > rec.StartPage {
			fmt.Fprintf(&b, " （第%d–%d页）", rec.StartPage, rec.EndPage)
		} else if rec.StartPage > 0 {
			fmt.Fprintf(&b, " （第%d页）", rec.StartPage)
		}
		b.WriteString(" ====================\n\n")
		b.WriteString(body)
	}

	name := safeName(bookName) + "_全本.txt"
	path := filepath.Join(dir, name)
	return path, os.WriteFile(path, []byte(b.String()), 0o644)
}

func nowStamp() string { return time.Now().Format("2006-01-02 15:04:05") }
