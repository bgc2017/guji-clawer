package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// 站点正文在 SSR 里始终是原书 OCR 字形（异体字很多），
// 阅读器显示时才在浏览器端做转换，站点提供三档：
//
//	原文 (orig)  —— 原样保留异体字
//	标准繁体 (trad) —— 异体字归一为标准繁体
//	机器简体 (simp) —— 再转成简体
//
// 这里把站点前端 bundle 里的两张转换表抄下来内置，离线复现同样的结果：
//
//	data/s2t.json  简/异体 → 标准繁体
//	data/t2s.json  繁/异体 → 简体（带词组表）
//
//go:embed data/s2t.json data/t2s.json
var dictFS embed.FS

type dictFile struct {
	Chars   string            `json:"chars"`
	Phrases map[string]string `json:"phrases"`
}

type converter struct {
	chars       map[rune]rune
	phraseSrc   [][]rune
	phraseDst   []string
	phraseMax   int
	phraseFirst map[rune][]int
	description string
}

var textConv *converter

// currentTextMode 记录本站使用的显示模式，浏览器里同步设置站点自己的 cookie，
// 让页面（以及 DOM 兜底）与 -text 保持一致：0 繁体 / 1 简体 / 2 原文。
var currentTextMode = "trad"

func loadConverter(path, desc string) (*converter, error) {
	data, err := dictFS.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f dictFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("解析 %s 失败: %w", path, err)
	}

	c := &converter{chars: decodeSparse(f.Chars), description: desc, phraseFirst: map[rune][]int{}}

	// 词组优先于单字，按长度从长到短匹配（站点前端用正则交替，等价于最长匹配）
	keys := make([]string, 0, len(f.Phrases))
	for k := range f.Phrases {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := []rune(keys[i]), []rune(keys[j])
		if len(a) != len(b) {
			return len(a) > len(b)
		}
		return keys[i] < keys[j]
	})
	for _, k := range keys {
		idx := len(c.phraseSrc)
		runes := []rune(k)
		c.phraseSrc = append(c.phraseSrc, runes)
		c.phraseDst = append(c.phraseDst, f.Phrases[k])
		c.phraseFirst[runes[0]] = append(c.phraseFirst[runes[0]], idx)
		if n := len(runes); n > c.phraseMax {
			c.phraseMax = n
		}
	}
	return c, nil
}

// decodeSparse 还原站点压缩过的映射表：
// 字符串由「间隔数字 + 一个汉字」组成，间隔数字是相对上一个字的位置偏移，
// 位置恰好等于 Unicode 码位，所以解出来就是 map[码位]目标字。
func decodeSparse(s string) map[rune]rune {
	out := make(map[rune]rune, len(s)/2)
	pos := 0
	gap := 0
	hasGap := false
	for _, ch := range s {
		if ch >= '0' && ch <= '9' {
			gap = gap*10 + int(ch-'0')
			hasGap = true
			continue
		}
		if hasGap {
			pos += gap
			gap = 0
			hasGap = false
		}
		out[rune(pos)] = ch
		pos++
	}
	return out
}

// Convert 按站点规则转换整段文字：命中词组就整词替换，否则逐字查表。
func (c *converter) Convert(s string) string {
	if c == nil || s == "" {
		return s
	}
	rs := []rune(s)
	var b strings.Builder
	b.Grow(len(s))

	for i := 0; i < len(rs); {
		matched := false
		for _, k := range c.phraseFirst[rs[i]] {
			src := c.phraseSrc[k]
			if i+len(src) > len(rs) {
				continue
			}
			same := true
			for j := range src {
				if rs[i+j] != src[j] {
					same = false
					break
				}
			}
			if same {
				b.WriteString(c.phraseDst[k])
				i += len(src)
				matched = true
				break
			}
		}
		if matched {
			continue
		}
		if t, ok := c.chars[rs[i]]; ok {
			b.WriteRune(t)
		} else {
			b.WriteRune(rs[i])
		}
		i++
	}
	return b.String()
}

// setTextMode 设定正文转换方式：orig 原文 / trad 标准繁体 / simp 机器简体。
func setTextMode(mode string) error {
	switch mode {
	case "", "trad", "繁", "繁体", "標準繁體", "standard":
		mode = "trad"
	case "simp", "简", "简体", "簡體":
		mode = "simp"
	case "orig", "原文", "raw":
		textConv = nil
		currentTextMode = "orig"
		return nil
	default:
		return fmt.Errorf("未知的文本模式 %q（可选 orig / trad / simp）", mode)
	}

	currentTextMode = mode
	path := "data/s2t.json"
	desc := "标准繁体"
	if mode == "simp" {
		path = "data/t2s.json"
		desc = "机器简体"
	}
	c, err := loadConverter(path, desc)
	if err != nil {
		return err
	}
	textConv = c
	return nil
}

func convText(s string) string {
	if textConv == nil {
		return s
	}
	return textConv.Convert(s)
}

func convAll(lines []string) []string {
	if textConv == nil {
		return lines
	}
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = textConv.Convert(l)
	}
	return out
}

// textModeName 返回当前模式的可读名称，用于日志与清单。
func textModeName() string {
	if textConv == nil {
		return "原文"
	}
	return textConv.description
}

// textModeCookieValue 对应站点 __is_simplified cookie 的取值：
// 0 标准繁体、1 机器简体、2 原文。
func textModeCookieValue() string {
	switch currentTextMode {
	case "simp":
		return "1"
	case "orig":
		return "2"
	default:
		return "0"
	}
}
