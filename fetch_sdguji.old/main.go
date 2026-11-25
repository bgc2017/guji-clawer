package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func main() {
	// 先切控制台编码，否则 Windows 上 -h / 参数报错里的中文就已经乱码了
	initConsole()

	var (
		bookArg   string
		outRoot   string
		lang      string
		visible   bool
		images    bool
		delayMS   int
		timeoutS  int
		force     bool
		only      string
		from      string
		maxN      int
		listOnly  bool
		rawDump   bool
		verbose   bool
		textMode  string
		chrome    string
		userAgent string
		proxy     string
	)
	flag.StringVar(&bookArg, "book", "", "图书 URL 或 ID，例如 https://www.shidianguji.com/book/CADAL01023318")
	flag.StringVar(&outRoot, "out", "books", "输出根目录")
	flag.StringVar(&lang, "lang", "", "站点语言路径，默认从 URL 推断（zh / ens）")
	flag.BoolVar(&visible, "visible", false, "显示浏览器窗口（调试或遇验证码时）")
	flag.BoolVar(&images, "images", false, "加载图片（默认关闭以加速）")
	flag.IntVar(&delayMS, "delay", 800, "每章之间的间隔毫秒数")
	flag.IntVar(&timeoutS, "timeout", 90, "单页超时秒数")
	flag.BoolVar(&force, "force", false, "忽略已下载文件，强制重抓")
	flag.StringVar(&only, "only", "", "只抓指定章：ID 或标题关键词，逗号分隔")
	flag.StringVar(&from, "from", "", "从指定章开始：ID 或标题关键词")
	flag.IntVar(&maxN, "max", 0, "最多抓多少章（调试用）")
	flag.BoolVar(&listOnly, "list", false, "只打印目录，不下载")
	flag.BoolVar(&rawDump, "raw", false, "每章额外保存原始段落 JSON")
	flag.BoolVar(&verbose, "v", false, "输出调试信息")
	flag.StringVar(&textMode, "text", "trad", "正文字形：trad 标准繁体（默认）/ simp 机器简体 / orig 站点原文")
	flag.StringVar(&chrome, "chrome", "", "浏览器可执行文件路径（默认自动探测，Windows 如 C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe）")
	flag.StringVar(&userAgent, "ua", "", "自定义 User-Agent（默认按当前系统生成）")
	flag.StringVar(&proxy, "proxy", "", "浏览器代理地址，例如 http://127.0.0.1:7890")
	flag.Parse()

	if err := setTextMode(textMode); err != nil {
		fatal(err)
	}

	if strings.TrimSpace(bookArg) == "" {
		flag.Usage()
		os.Exit(2)
	}

	bookID, inferredLang, err := ParseBookArg(bookArg)
	if err != nil {
		fatal(err)
	}
	if lang == "" {
		lang = inferredLang
	}

	b, err := NewBrowser(browserOptions{
		visible:    visible,
		images:     images,
		timeout:    time.Duration(timeoutS) * time.Second,
		lang:       lang,
		verbose:    verbose,
		chromePath: chrome,
		userAgent:  userAgent,
		proxy:      proxy,
	})
	if err != nil {
		fatal(err)
	}
	defer b.Close()

	// Ctrl+C（Windows 下也适用）：先关浏览器再退出，避免留下无头 chrome 进程
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	go func() {
		<-sigCh
		fmt.Println("\n已中断，正在关闭浏览器…")
		b.Close()
		os.Exit(1)
	}()

	start := time.Now()
	fmt.Printf("站点: shidianguji.com  图书: %s  语言: %s\n", bookID, lang)

	// 1) 找到全书第一章
	sc, err := b.DiscoverStartChapter(bookID)
	if err != nil {
		fatal(err)
	}

	// 2) 用第一章的 SSR 数据建立完整书目（目录也在里面）
	first, err := b.FetchChapter(bookID, sc.ChapterID, true)
	if err != nil {
		fatal(fmt.Errorf("读取第一章失败: %w", err))
	}
	if first.BookID != "" {
		bookID = first.BookID
	}

	bookName := first.BookName
	if bookName == "" {
		bookName = sc.Title
	}
	var catalog []*Chapter
	if first.Catalog != nil {
		catalog = first.Catalog.Chapters
	}
	if len(catalog) == 0 {
		catalog = []*Chapter{{ChapterID: sc.ChapterID, ChapterName: []ChapterName{{Content: sc.Title}}}}
	}

	author := authorLine(&BookInfo{
		Authors:             first.Authors,
		DynastyCategoryName: first.Dynasty,
	})
	totalPages := first.TotalPage
	fmt.Printf("书目: 《%s》 %s，共 %d 章 / %d 页\n", bookName, author, countChapters(catalog), totalPages)
	fmt.Printf("正文: %s\n", textModeName())

	// 3) 输出目录与书目清单
	dir := filepath.Join(outRoot, safeName(bookID+"-"+bookName))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fatal(err)
	}
	if rawDump {
		_ = os.MkdirAll(filepath.Join(dir, "raw"), 0o755)
	}

	all := flattenChapters(catalog)
	records := make([]ChapterRecord, 0, len(all))
	for i, c := range all {
		s, e := c.PageRange()
		records = append(records, ChapterRecord{
			Index:     i + 1,
			ID:        c.ChapterID,
			Title:     c.Title(),
			StartPage: s,
			EndPage:   e,
			Expected:  c.ParagraphCount,
			File:      fmt.Sprintf("%03d_%s.txt", i+1, safeName(c.Title())),
		})
	}

	m := &Manifest{
		BookID:    bookID,
		BookName:  bookName,
		Author:    author,
		Dynasty:   first.Dynasty,
		SourceURL: b.bookURL(bookID),
		TotalPage: totalPages,
		Chapters:  records,
		CrawledAt: nowStamp(),
		TextMode:  textModeName(),
		Version:   "2.1",
	}

	// 读取上次的清单：断点续抓时要按「每章记录的文本模式」判断旧文件是否还能用，
	// 换了 -text 后旧字形的文件不能被当成已缓存跳过。
	if raw, err := os.ReadFile(filepath.Join(dir, "book.json")); err == nil {
		var old Manifest
		if json.Unmarshal(raw, &old) == nil {
			oldModes := make(map[string]string, len(old.Chapters))
			for _, r := range old.Chapters {
				if r.TextMode != "" {
					oldModes[r.ID] = r.TextMode
				}
			}
			need := 0
			for i := range records {
				if mode, ok := oldModes[records[i].ID]; ok {
					records[i].TextMode = mode
					if mode != m.TextMode {
						need++
					}
				}
			}
			if need > 0 && !listOnly {
				fmt.Printf("本地有 %d 章是旧字形（%s），将用「%s」重新抓取\n", need, old.TextMode, m.TextMode)
			}
		}
	}

	// 4) 选择要抓的章节范围
	sel, err := selectChapters(all, records, only, from, maxN)
	if err != nil {
		fatal(err)
	}

	if listOnly {
		fmt.Println()
		for _, i := range sel {
			r := records[i]
			fmt.Printf("  [%03d] %-4d–%-4d页  %s  (%s, 约%d段)\n", r.Index, r.StartPage, r.EndPage, r.Title, r.ID, r.Expected)
		}
		fmt.Printf("\n共 %d 章，目录已保存在: %s\n", len(sel), filepath.Join(dir, "book.json"))
		_ = saveJSON(filepath.Join(dir, "book.json"), m)
		return
	}

	saveJSON(filepath.Join(dir, "book.json"), m)

	// 5) 逐章抓取（第一章已抓过，直接复用它）
	var (
		failed   []string
		doneN    int
		cachedN  int
		sumChars int
	)
	for idxNo, recIdx := range sel {
		rec := &m.Chapters[recIdx]
		ch := all[recIdx]
		path := filepath.Join(dir, rec.File)
		label := fmt.Sprintf("[%d/%d] %s", idxNo+1, len(sel), rec.Title)

		// 断点续抓：文件存在、且记录的文本模式与本次一致才跳过
		if !force && rec.TextMode == m.TextMode {
			if st, err := os.Stat(path); err == nil && st.Size() > 0 {
				rec.Chars, rec.Paragraphs = countFileChars(path)
				rec.Status = "cached"
				cachedN++
				sumChars += rec.Chars
				fmt.Printf("%s  已存在，跳过（%d字）\n", label, rec.Chars)
				continue
			}
		}

		t0 := time.Now()
		var (
			paras  []string
			source string
			curCD  *ChapterData
			werr   error
		)

		if rec.ID == first.ChapterID {
			curCD = first
			paras = chapterText(ch, first.Paras)
			source = "SSR"
		} else {
			cd, ferr := b.FetchChapter(bookID, rec.ID, false)
			if ferr != nil {
				if b.verbose {
					fmt.Printf("%s  章节数据失败: %v，改用逐页兜底\n", label, ferr)
				}
				if !ch.hasSubs() {
					if ps, e := b.CrawlByPages(bookID, rec.ID); e == nil && len(ps) > 0 {
						paras, source = ps, "DOM"
					}
				}
				if len(paras) == 0 {
					rec.Status = "failed"
					rec.Note = ferr.Error()
					rec.ElapsedMS = time.Since(t0).Milliseconds()
					failed = append(failed, rec.Title)
					fmt.Printf("%s  失败: %v\n", label, ferr)
					saveJSON(filepath.Join(dir, "book.json"), m)
					sleepDelay(delayMS)
					continue
				}
			} else {
				curCD = cd
				paras = chapterText(ch, cd.Paras)
				source = "SSR"
			}
		}

		// 段落数按“去掉重复标题之前”的数量核对，否则每章都会少算一条
		nRaw := len(paras)
		if nRaw > 1 {
			paras = stripLeadingTitle(paras, rec.Title)
		}
		paras = convAll(paras)

		if len(paras) == 0 {
			rec.Status = "empty"
			rec.Note = "本章无文本（可能是纯图像章节）"
			rec.ElapsedMS = time.Since(t0).Milliseconds()
			fmt.Printf("%s  无正文\n", label)
			saveJSON(filepath.Join(dir, "book.json"), m)
			sleepDelay(delayMS)
			continue
		}

		if rec.Expected > 0 && nRaw < rec.Expected && source == "SSR" {
			rec.Note = fmt.Sprintf("段落数少于目录标注（%d/%d），可能被截断", nRaw, rec.Expected)
		}

		rec.Status = "done"
		rec.TextMode = m.TextMode
		if _, werr = writeChapterFile(path, rec, paras); werr != nil {
			rec.Status = "failed"
			rec.Note = werr.Error()
			failed = append(failed, rec.Title)
			fmt.Printf("%s  写文件失败: %v\n", label, werr)
			saveJSON(filepath.Join(dir, "book.json"), m)
			continue
		}
		rec.ElapsedMS = time.Since(t0).Milliseconds()
		doneN++
		sumChars += rec.Chars

		if rawDump {
			dumpRaw(filepath.Join(dir, "raw", strings.TrimSuffix(rec.File, ".txt")+".json"), rec, curCD)
		}

		fmt.Printf("%s  %d字 %d段  %.1fs  (%s)\n", label, rec.Chars, rec.Paragraphs, float64(rec.ElapsedMS)/1000, source)
		saveJSON(filepath.Join(dir, "book.json"), m)
		sleepDelay(delayMS)
	}

	// 6) 汇总全本
	combined := ""
	if doneN+cachedN > 0 {
		if p, err := writeCombined(dir, bookName, author, m); err == nil {
			combined = p
		} else {
			fmt.Println("写全本失败:", err)
		}
	}
	m.CrawledAt = nowStamp()
	saveJSON(filepath.Join(dir, "book.json"), m)

	fmt.Println()
	fmt.Printf("完成：新抓 %d 章，跳过 %d 章，失败 %d 章，共 %d 字，用时 %s\n",
		doneN, cachedN, len(failed), sumChars, time.Since(start).Round(time.Second))
	fmt.Println("目录:", dir)
	if combined != "" {
		fmt.Println("全本:", combined)
	}
	if len(failed) > 0 {
		sort.Strings(failed)
		fmt.Printf("失败章节：%s\n", strings.Join(failed, "、"))
		os.Exit(1)
	}
}

func fatal(err error) {
	fmt.Println("错误:", err)
	os.Exit(2)
}

func sleepDelay(ms int) {
	if ms <= 0 {
		return
	}
	jitter := float64(ms) * (0.75 + rand.Float64()*0.5)
	time.Sleep(time.Duration(jitter) * time.Millisecond)
}

// chapterText 从段落中提取纯文本；父章（有子章）只保留属于自身的段落，避免与子章重复。
func chapterText(ch *Chapter, paras []Para) []string {
	var picked []Para
	if ch.hasSubs() {
		for _, p := range paras {
			if p.CID == ch.ChapterID {
				picked = append(picked, p)
			}
		}
		if len(picked) == 0 {
			n := ch.ParagraphCount
			if n <= 0 || n > len(paras) {
				n = len(paras)
			}
			picked = paras[:n]
		}
	} else {
		picked = paras
	}

	out := make([]string, 0, len(picked))
	for _, p := range picked {
		if t := strings.TrimSpace(p.Text); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// stripLeadingTitle 去掉正文开头与章节标题重复的那一段。
// 正文里往往再次排版书名/卷名，而落盘文件已写了标题行，重复出现只影响阅读。
func stripLeadingTitle(paras []string, title string) []string {
	norm := func(s string) string { return strings.Join(strings.Fields(s), "") }
	t := norm(title)
	if t == "" {
		return paras
	}
	for len(paras) > 0 && norm(paras[0]) == t {
		paras = paras[1:]
	}
	return paras
}

// selectChapters 按 -only / -from / -max 返回需要抓取的记录下标。
// matchChapters 解析 -only / -from 的关键词。先按完整标题或章节 ID 精确匹配，
// 没有精确命中时才退回包含匹配，避免“卷一”把“卷十一”一起选中。
func matchChapters(records []ChapterRecord, kw string) map[int]bool {
	var pieces []string
	for _, piece := range strings.Split(kw, ",") {
		if p := strings.TrimSpace(piece); p != "" {
			pieces = append(pieces, p)
		}
	}
	if len(pieces) == 0 {
		return nil
	}
	norm := func(s string) string { return strings.Join(strings.Fields(s), "") }

	out := map[int]bool{}
	for _, p := range pieces {
		pn := norm(p)
		exact := map[int]bool{}
		suffix := map[int]bool{}
		contains := map[int]bool{}
		for i, r := range records {
			tn := norm(r.Title)
			tc := norm(convText(r.Title))
			switch {
			case p == r.ID || pn == tn || pn == tc:
				exact[i] = true
			case strings.HasSuffix(tn, pn) || strings.HasSuffix(tc, pn):
				suffix[i] = true
			}
			if strings.Contains(tn, pn) || strings.Contains(tc, pn) {
				contains[i] = true
			}
		}
		switch {
		case len(exact) > 0:
			for i := range exact {
				out[i] = true
			}
		case len(suffix) > 0:
			for i := range suffix {
				out[i] = true
			}
		default:
			for i := range contains {
				out[i] = true
			}
		}
	}
	return out
}

func selectChapters(all []*Chapter, records []ChapterRecord, only, from string, maxN int) ([]int, error) {
	onlySet := matchChapters(records, only)
	fromSet := matchChapters(records, from)

	var sel []int
	for i := range all {
		if onlySet != nil && !onlySet[i] {
			continue
		}
		if fromSet != nil && len(sel) == 0 && !fromSet[i] {
			// 还没到起始章，先跳过；一旦命中就后续全部保留
			continue
		}
		sel = append(sel, i)
	}

	if maxN > 0 && len(sel) > maxN {
		sel = sel[:maxN]
	}
	if len(sel) == 0 {
		return nil, fmt.Errorf("没有匹配到要抓取的章节（-only=%q -from=%q）", only, from)
	}
	return sel, nil
}

func dumpRaw(path string, rec *ChapterRecord, cd *ChapterData) {
	if cd == nil {
		return
	}
	_ = saveJSON(path, map[string]any{
		"chapter_id":   rec.ID,
		"title":        rec.Title,
		"book_id":      cd.BookID,
		"book_name":    cd.BookName,
		"chapter_page": cd.ChapterID,
		"paras":        cd.Paras,
	})
}
