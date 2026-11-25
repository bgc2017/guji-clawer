package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

// 默认 UA 按运行平台生成，Windows / macOS / Linux 各用各的，避免和真实系统不一致。
func defaultUserAgent() string {
	switch runtime.GOOS {
	case "windows":
		return "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
	case "darwin":
		return "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
	default:
		return "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
	}
}

// 浏览器候选路径：按当前系统给，Windows 下包括 Chrome 与 Edge（都基于 Chromium）。
func chromeCandidates() []string {
	switch runtime.GOOS {
	case "windows":
		var list []string
		add := func(dir, exe string) {
			if dir != "" {
				list = append(list, filepath.Join(dir, exe))
			}
		}
		pf := os.Getenv("ProgramFiles")
		pf86 := os.Getenv("ProgramFiles(x86)")
		local := os.Getenv("LOCALAPPDATA")
		add(pf, `Google\Chrome\Application\chrome.exe`)
		add(pf86, `Google\Chrome\Application\chrome.exe`)
		add(local, `Google\Chrome\Application\chrome.exe`)
		add(pf86, `Microsoft\Edge\Application\msedge.exe`)
		add(pf, `Microsoft\Edge\Application\msedge.exe`)
		// 少数机器装在默认路径但环境变量缺失
		list = append(list,
			`C:\Program Files\Google\Chrome\Application\chrome.exe`,
			`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
		)
		return list
	case "darwin":
		return []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
		}
	default:
		return []string{
			"/usr/bin/google-chrome",
			"/usr/bin/google-chrome-stable",
			"/usr/bin/chromium",
			"/usr/bin/chromium-browser",
		}
	}
}

// findChrome 的优先级：命令行 -chrome > 环境变量 SDG_CHROME > 系统常见路径。
func findChrome(explicit string) (string, error) {
	if explicit != "" {
		if _, err := os.Stat(explicit); err != nil {
			return "", fmt.Errorf("指定的浏览器路径不可用: %s", explicit)
		}
		return explicit, nil
	}
	if p := os.Getenv("SDG_CHROME"); p != "" {
		if _, err := os.Stat(p); err != nil {
			return "", fmt.Errorf("环境变量 SDG_CHROME 指向的文件不存在: %s", p)
		}
		return p, nil
	}
	cands := chromeCandidates()
	for _, p := range cands {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	hint := "未找到 Chrome 或 Edge，请安装 Chrome，或用 -chrome 参数 / 环境变量 SDG_CHROME 指定路径"
	if len(cands) > 0 {
		hint += "\n已尝试: " + strings.Join(cands, "\n        ")
	}
	return "", fmt.Errorf("%s", hint)
}

// Browser 封装一个真实的 Chrome 标签页，所有请求都由浏览器自己发出。
type Browser struct {
	ctx     context.Context
	cancel  context.CancelFunc
	timeout time.Duration
	lang    string
	verbose bool

	docMu sync.Mutex
	docs  map[string]network.RequestID

	// 本章累计下载流量（字节），只用于 -v 时报告，不做任何限速。
	netBytes atomic.Int64
}

type browserOptions struct {
	visible    bool
	images     bool
	noCache    bool
	blockFonts bool
	timeout    time.Duration
	lang       string
	verbose    bool
	chromePath string
	userAgent  string
	proxy      string
}

func NewBrowser(o browserOptions) (*Browser, error) {
	path, err := findChrome(o.chromePath)
	if err != nil {
		return nil, err
	}

	ua := o.userAgent
	if ua == "" {
		ua = defaultUserAgent()
	}

	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(path),
		chromedp.UserAgent(ua),
		chromedp.WindowSize(1440, 960),
		chromedp.Flag("enable-automation", false),
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
		chromedp.Flag("lang", "zh-CN"),
		chromedp.Flag("no-first-run", true),
		chromedp.Flag("disable-notifications", true),
	)
	if o.proxy != "" {
		opts = append(opts, chromedp.ProxyServer(o.proxy))
	}
	if o.verbose {
		fmt.Printf("浏览器: %s\nUA: %s\n", path, ua)
		if o.proxy != "" {
			fmt.Printf("代理: %s\n", o.proxy)
		}
	}
	if o.visible {
		// 去掉 headless 等隐藏窗口的开关
		opts = append(opts, chromedp.VisibleWindow)
	}
	if !o.images {
		opts = append(opts, chromedp.Flag("blink-settings", "imagesEnabled=false"))
	}
	if runtime.GOOS == "linux" {
		opts = append(opts, chromedp.Flag("no-sandbox", true))
	}

	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), opts...)
	ctx, cancel := chromedp.NewContext(allocCtx)

	// 第一次 Run 拉起浏览器；这里不设总超时（总超时会连同浏览器一起结束）。
	if err := chromedp.Do(ctx, chromedp.Navigate("about:blank")); err != nil {
		cancel()
		allocCancel()
		return nil, fmt.Errorf("启动浏览器失败: %w", err)
	}

	b := &Browser{
		ctx:     ctx,
		timeout: o.timeout,
		lang:    o.lang,
		verbose: o.verbose,
		docs:    make(map[string]network.RequestID),
	}
	b.cancel = func() {
		cancel()
		allocCancel()
	}

	// 记录每个文档请求的 requestId，后面用 CDP 直接取回导航响应的原始 HTML（SSR 数据）。
	_ = chromedp.Listen(ctx, func(ev any) {
		switch e := ev.(type) {
		case *network.EventRequestWillBeSent:
			if e.Type == network.ResourceTypeDocument {
				b.docMu.Lock()
				b.docs[e.Request.URL] = e.RequestID
				b.docMu.Unlock()
			}
		case *network.EventLoadingFinished:
			b.netBytes.Add(int64(e.EncodedDataLength))
		}
	})

	// 禁用缓存，保证每次都能拿到完整的响应体。
	_ = chromedp.Do(ctx, chromedp.Func(func(c context.Context, t *chromedp.Target) error {
		if _, err := cdp.Call(c, t, network.Enable, network.EnableParams{}); err != nil {
			return err
		}
		// 同步站点的字形偏好：__is_simplified 0=标准繁体 1=机器简体 2=原文。
		// 这个 cookie 只影响阅读器显示与页面元信息，SSR 正文仍是原书字形，
		// 所以正文转换仍由本地字典完成（见 convert.go）。
		cookie := network.SetCookieParams{
			Name:   "__is_simplified",
			Value:  textModeCookieValue(),
			Domain: "shidianguji.com",
			Path:   "/",
		}
		if _, err := cdp.Call(c, t, network.SetCookie, cookie); err != nil {
			return err
		}
		// 默认保留浏览器缓存：章节页本身每次都不同（必须走网络），但站点的 JS/CSS/字体
		// 是同一批静态资源。禁用缓存会让每一章都重下约 15MB（9MB JS + 6MB 字体），
		// 打开缓存后第一节约 15MB、之后每节约 0.15MB。排查问题时可用 -no-cache 关掉。
		if _, err := cdp.Call(c, t, network.SetCacheDisabled, network.SetCacheDisabledParams{CacheDisabled: o.noCache}); err != nil {
			return err
		}
		if o.blockFonts {
			// 章节正文来自 SSR HTML，不需要网页字体渲染；屏蔽字体可再省 0.2–6MB/章。
			patterns := []*network.BlockPattern{}
			for _, ext := range []string{"woff2", "woff", "ttf", "otf"} {
				patterns = append(patterns, &network.BlockPattern{URLPattern: "*://*/*." + ext, Block: true})
			}
			if _, err := cdp.Call(c, t, network.SetBlockedURLs, network.SetBlockedURLsParams{URLPatterns: patterns}); err != nil {
				if o.verbose {
					fmt.Fprintf(os.Stderr, "屏蔽字体失败（浏览器版本可能不支持）: %v\n", err)
				}
			}
		}
		return nil
	}))

	return b, nil
}

func (b *Browser) Close() { b.cancel() }

func (b *Browser) newOpCtx() (context.Context, context.CancelFunc) {
	t := b.timeout
	if t <= 0 {
		t = 90 * time.Second
	}
	return context.WithTimeout(b.ctx, t)
}

func (b *Browser) bookURL(bookID string) string {
	return fmt.Sprintf("https://www.shidianguji.com/%s/book/%s", b.lang, bookID)
}

func (b *Browser) chapterURL(bookID, chapterID string) string {
	return fmt.Sprintf("https://www.shidianguji.com/%s/book/%s/chapter/%s", b.lang, bookID, chapterID)
}

func (b *Browser) navigate(url string) error {
	ctx, cancel := b.newOpCtx()
	defer cancel()
	return chromedp.Do(ctx, chromedp.Navigate(url))
}

func (b *Browser) evalInto(expr string, out any) error {
	ctx, cancel := b.newOpCtx()
	defer cancel()
	raw, err := chromedp.Run(ctx, chromedp.Evaluate[map[string]any](expr))
	if err != nil {
		return err
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

func (b *Browser) logf(format string, args ...any) {
	if b.verbose {
		fmt.Printf("  · "+format+"\n", args...)
	}
}

// ---------- 图书详情页：定位第一章 ----------

type startChapter struct {
	ChapterID string `json:"chapterId"`
	Title     string `json:"title"`
}

func (b *Browser) DiscoverStartChapter(bookID string) (*startChapter, error) {
	url := b.bookURL(bookID)
	if err := b.navigate(url); err != nil {
		return nil, fmt.Errorf("打开图书页失败: %w", err)
	}

	for attempt := 1; attempt <= 3; attempt++ {
		ctx, cancel := b.newOpCtx()
		raw, err := chromedp.Run(ctx, chromedp.Poll[map[string]any](exprFindStartChapter,
			chromedp.WithPollingInterval(400*time.Millisecond),
			chromedp.WithPollingTimeout(20*time.Second)))
		cancel()
		if err == nil && raw != nil {
			id, _ := raw["chapterId"].(string)
			title, _ := raw["title"].(string)
			if id != "" {
				b.logf("目录首章: %s %s", id, title)
				return &startChapter{ChapterID: id, Title: title}, nil
			}
		}

		// 目录没出来时，尝试点“开始阅读”之类的入口
		if attempt == 1 {
			clk := b.clickStartRead()
			if clk {
				b.logf("已点击阅读入口，等待跳转")
				if s := b.waitChapterInURL(); s != nil {
					return s, nil
				}
			}
		}
		// 重新加载一次，绕开偶发风控/接口失败
		if attempt < 3 {
			_ = b.navigate(url)
		}
	}
	return nil, fmt.Errorf("未能在图书页找到目录（可加 -visible 观察页面，或确认图书是否存在）")
}

func (b *Browser) clickStartRead() bool {
	expr := `(() => {
	  const cands = [...document.querySelectorAll('a,button,[role=button],div')];
	  const el = cands.find(e => /^(开始阅读|继续阅读|在线阅读|立即阅读|阅读)$/.test((e.innerText||'').trim()));
	  if (!el) return false;
	  el.click();
	  return true;
	})()`
	ctx, cancel := b.newOpCtx()
	defer cancel()
	ok, err := chromedp.Run(ctx, chromedp.Evaluate[bool](expr))
	return err == nil && ok
}

func (b *Browser) waitChapterInURL() *startChapter {
	expr := `(() => {
	  const m = location.href.match(/\/chapter\/([^/?#]+)/);
	  return m ? {chapterId: m[1], title: document.title || ''} : null;
	})()`
	ctx, cancel := b.newOpCtx()
	defer cancel()
	raw, err := chromedp.Run(ctx, chromedp.Poll[map[string]any](expr,
		chromedp.WithPollingInterval(400*time.Millisecond),
		chromedp.WithPollingTimeout(15*time.Second)))
	if err != nil || raw == nil {
		return nil
	}
	id, _ := raw["chapterId"].(string)
	if id == "" {
		return nil
	}
	return &startChapter{ChapterID: id, Title: fmt.Sprintf("%v", raw["title"])}
}

// ---------- 章节页：取整章数据 ----------

// FetchChapter 打开章节页，并从浏览器收到的导航响应（SSR HTML）里解析整章正文。
// 直接读响应体，而不是读 window._ROUTER_DATA：后者会在前端 hydration 后
// 被替换成客户端数据，抓取时机稍晚就只剩空壳。
func (b *Browser) FetchChapter(bookID, chapterID string, wantCatalog bool) (*ChapterData, error) {
	url := b.chapterURL(bookID, chapterID)
	b.netBytes.Store(0)

	var lastErr error
	for attempt := 1; attempt <= 4; attempt++ {
		b.forgetDoc(url)

		if err := b.navigate(url); err != nil {
			lastErr = fmt.Errorf("打开章节页失败: %w", err)
			b.backoff(attempt)
			continue
		}

		rid := b.docReqID(url)
		if rid == "" {
			lastErr = fmt.Errorf("未捕获到章节页的文档请求")
			b.backoff(attempt)
			continue
		}

		ctx, cancel := b.newOpCtx()
		var body []byte
		err := chromedp.Do(ctx, func(c context.Context, t *chromedp.Target) (chromedp.Void, error) {
			res, gerr := cdp.Call(c, t, network.GetResponseBody, network.GetResponseBodyParams{RequestID: rid})
			if gerr == nil {
				body = res.Body
			}
			return chromedp.Void{}, gerr
		})
		cancel()
		if err != nil {
			lastErr = fmt.Errorf("读取页面响应失败: %w", err)
			b.backoff(attempt)
			continue
		}

		cd, perr := parseChapterHTML(body, chapterID, wantCatalog)
		if perr == nil {
			if b.verbose {
				b.logf("本章流量: %.2f MB", float64(b.netBytes.Load())/1e6)
			}
			if b.verbose && cd.ChapterID != chapterID {
				b.logf("注意：请求章节 %s，服务端返回 %s", chapterID, cd.ChapterID)
			}
			return cd, nil
		}
		lastErr = perr

		if b.verbose {
			var diag struct {
				URL   string `json:"url"`
				Title string `json:"title"`
				Body  string `json:"body"`
			}
			if e := b.evalInto(exprPageDiag, &diag); e == nil {
				b.logf("页面状态: %s | %s | %s", diag.URL, diag.Title, trimRunes(diag.Body, 120))
			}
		}
		b.backoff(attempt)
	}
	return nil, lastErr
}

func (b *Browser) forgetDoc(url string) {
	b.docMu.Lock()
	delete(b.docs, url)
	b.docMu.Unlock()
}

func (b *Browser) docReqID(url string) network.RequestID {
	b.docMu.Lock()
	defer b.docMu.Unlock()
	return b.docs[url]
}

func (b *Browser) backoff(attempt int) {
	d := time.Duration(attempt*attempt) * 1500 * time.Millisecond
	if d > 20*time.Second {
		d = 20 * time.Second
	}
	time.Sleep(d)
}

// ---------- DOM 兜底：逐页翻到本章末尾 ----------

// CrawlByPages 用于 SSR 数据缺失或明显截断时：点击“下一页”直到本章最后一页，
// 阅读区会把已加载的正文累积在 DOM 中，取段落最多的快照。
func (b *Browser) CrawlByPages(bookID, chapterID string) ([]string, error) {
	if err := b.navigate(b.chapterURL(bookID, chapterID)); err != nil {
		return nil, err
	}

	ctx, cancel := b.newOpCtx()
	defer cancel()

	type art struct {
		Ps    []string `json:"ps"`
		Cur   int      `json:"cur"`
		Total int      `json:"total"`
	}
	read := func() (*art, error) {
		var out art
		raw, err := chromedp.Run(ctx, chromedp.Poll[map[string]any](exprArticle,
			chromedp.WithPollingInterval(500*time.Millisecond),
			chromedp.WithPollingTimeout(20*time.Second)))
		if err != nil {
			return nil, err
		}
		data, _ := json.Marshal(raw)
		if err := json.Unmarshal(data, &out); err != nil {
			return nil, err
		}
		return &out, nil
	}

	best, err := read()
	if err != nil {
		return nil, err
	}
	bestLen := len(best.Ps)

	stall := 0
	for i := 0; i < 2000; i++ {
		if best.Total > 0 && best.Cur >= best.Total {
			break
		}
		err := chromedp.Do(ctx, chromedp.Click(chromedp.CSS(`.vik-toolbox-btn[type="page-next"]`)))
		if err != nil {
			// 没有翻页控件，可能单页章节
			break
		}
		time.Sleep(900 * time.Millisecond)

		snap, err := read()
		if err != nil {
			break
		}
		if snap.Cur == best.Cur {
			stall++
			if stall >= 3 {
				break
			}
		} else {
			stall = 0
		}
		if len(snap.Ps) > bestLen {
			best, bestLen = snap, len(snap.Ps)
		} else {
			best.Cur, best.Total = snap.Cur, snap.Total
		}
		// 到末页后再读一次，确保最后一页正文已渲染
		if snap.Total > 0 && snap.Cur >= snap.Total {
			time.Sleep(500 * time.Millisecond)
			if last, e := read(); e == nil && len(last.Ps) >= bestLen {
				best = last
			}
			break
		}
	}
	return best.Ps, nil
}

var chapterIDRe = regexp.MustCompile(`chapter/([^/?#]+)`)
var bookIDRe = regexp.MustCompile(`book/([A-Za-z0-9]+)`)

// ParseBookArg 支持整条 URL（含 /zh/、/ens/、chapter 子路径）或裸 bookId。
func ParseBookArg(arg string) (bookID, lang string, err error) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return "", "", fmt.Errorf("图书参数为空")
	}
	if m := bookIDRe.FindStringSubmatch(arg); len(m) == 2 {
		bookID = m[1]
	} else if regexp.MustCompile(`^[A-Za-z0-9]+$`).MatchString(arg) {
		bookID = arg
	} else {
		return "", "", fmt.Errorf("无法从 %q 解析出图书 ID", arg)
	}
	lang = "zh"
	for _, p := range []string{"zh", "ens", "bo", "zh-Hant", "zh-Hans"} {
		if strings.Contains(arg, "/"+p+"/") {
			lang = p
			break
		}
	}
	return bookID, lang, nil
}

func chapterIDFromArg(arg string) string {
	if m := chapterIDRe.FindStringSubmatch(arg); len(m) == 2 {
		return m[1]
	}
	return ""
}

// trimRunes 截断字符串到指定字符数（按 rune 计）。
func trimRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
