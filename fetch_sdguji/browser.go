package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

const defaultUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

// Browser 封装一个真实的 Chrome 标签页，所有请求都由浏览器自己发出。
type Browser struct {
	ctx     context.Context
	cancel  context.CancelFunc
	timeout time.Duration
	lang    string
	verbose bool

	docMu sync.Mutex
	docs  map[string]network.RequestID
}

type browserOptions struct {
	visible bool
	images  bool
	timeout time.Duration
	lang    string
	verbose bool
}

// 常见 Chrome/Chromium 安装位置
var chromePaths = []string{
	"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
	"/Applications/Chromium.app/Contents/MacOS/Chromium",
	"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
	"/usr/bin/google-chrome",
	"/usr/bin/google-chrome-stable",
	"/usr/bin/chromium",
	"/usr/bin/chromium-browser",
	`C:\Program Files\Google\Chrome\Application\chrome.exe`,
	`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
}

func findChrome() string {
	if p := os.Getenv("SDG_CHROME"); p != "" {
		return p
	}
	for _, p := range chromePaths {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func NewBrowser(o browserOptions) (*Browser, error) {
	path := findChrome()
	if path == "" {
		return nil, fmt.Errorf("未找到 Chrome/Chromium，请安装，或用环境变量 SDG_CHROME 指定可执行文件路径")
	}

	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(path),
		chromedp.UserAgent(defaultUA),
		chromedp.WindowSize(1440, 960),
		chromedp.Flag("enable-automation", false),
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
		chromedp.Flag("lang", "zh-CN"),
		chromedp.Flag("no-first-run", true),
		chromedp.Flag("disable-notifications", true),
	)
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
		if e, ok := ev.(*network.EventRequestWillBeSent); ok && e.Type == network.ResourceTypeDocument {
			b.docMu.Lock()
			b.docs[e.Request.URL] = e.RequestID
			b.docMu.Unlock()
		}
	})

	// 禁用缓存，保证每次都能拿到完整的响应体。
	_ = chromedp.Do(ctx, chromedp.Func(func(c context.Context, t *chromedp.Target) error {
		if _, err := cdp.Call(c, t, network.Enable, network.EnableParams{}); err != nil {
			return err
		}
		_, err := cdp.Call(c, t, network.SetCacheDisabled, network.SetCacheDisabledParams{CacheDisabled: true})
		return err
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
