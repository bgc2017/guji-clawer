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
	TextMode   string `json:"text_mode,omitempty"` // 该章文件实际使用的字形
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
	TextMode  string          `json:"text_mode"`
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
	// 去掉控制字符与结尾的点/空格（Windows 会拒绝对这类文件名做某些操作）
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	s = strings.TrimRight(s, ". ")
	// Windows 保留设备名不能直接当文件名
	switch strings.ToUpper(s) {
	case "CON", "PRN", "AUX", "NUL",
		"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		s += "_"
	}
	if s == "" {
		s = "untitled"
	}
	if utf8.RuneCountInString(s) > 60 {
		s = string([]rune(s)[:60])
	}
	return s
}

// withPlatformNL 统一换行：Windows 用 CRLF，其它系统用 LF。
// 先归一成 LF 再换，避免读进来的 CRLF 变成 \r\r\n。
func withPlatformNL(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	if platformNL() == "\n" {
		return s
	}
	return strings.ReplaceAll(s, "\n", platformNL())
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
	fmt.Fprintf(&b, "%s\n", convText(r.Title))
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
	if err := os.WriteFile(path, []byte(withPlatformNL(b.String())), 0o644); err != nil {
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
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
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
	fmt.Fprintf(&b, "《%s》\n", convText(bookName))
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
		fmt.Fprintf(&b, "==================== [%03d] %s", rec.Index, convText(rec.Title))
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
	return path, os.WriteFile(path, []byte(withPlatformNL(b.String())), 0o644)
}

func nowStamp() string { return time.Now().Format("2006-01-02 15:04:05") }
