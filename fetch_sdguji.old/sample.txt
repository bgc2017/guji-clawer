package main

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/chromedp/chromedp"
)

const baseURL = "https://kandianguji.com"

// 站点前端 AES-CBC 密钥（hex 字符串，需解码成 16 字节）
const (
	reqKeyHex  = "2d754b35322c5876755147644c684224"
	reqIVHex   = "6f6b35487d62505f794032364d72395d"
	respKeyHex = "2332606c376d7e6256765b2c47507425"
	respIVHex  = "532d3a78605a3972647e3f757e3c6c40"
)

const ua = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

type Page struct {
	Page    int    `json:"page"`
	Title   string `json:"title"`
	Text    string `json:"text"`
	OK      bool   `json:"ok"`
	Attempt int    `json:"attempt"`
}

type Volume struct {
	ID    string `json:"id"`
	Order int    `json:"order"`
	Title string `json:"title"`
	Total int    `json:"total"`
	Pages []Page `json:"pages"`
	Dir   string `json:"dir"`
}

type BookResult struct {
	BookID    string    `json:"book_id"`
	Title     string    `json:"title"`
	Author    string    `json:"author"`
	UpdatedAt string    `json:"updated_at"`
	Volumes   []*Volume `json:"volumes"`
}

type options struct {
	book        string
	out         string
	visible     bool
	delay       int
	startVolume string
	maxPage     int
	onlyVol     string
	noBrowser   bool
}

func main() {
	var o options
	flag.StringVar(&o.book, "book", "105674", "book id")
	flag.StringVar(&o.out, "out", "output", "output dir")
	flag.BoolVar(&o.visible, "visible", false, "显示浏览器窗口(调试用)")
	flag.IntVar(&o.delay, "delay", 1000, "每页请求间隔(毫秒)，过快会 429")
	flag.IntVar(&o.maxPage, "max-pages", 0, "调试:每卷最多抓几页")
	flag.StringVar(&o.onlyVol, "only-volume", "", "调试:只抓某一卷")
	flag.StringVar(&o.startVolume, "start-volume", "", "从某一卷开始(断点续抓)")
	flag.BoolVar(&o.noBrowser, "no-browser", false, "不启动浏览器(卷页数取已有 _volume.json 缓存)")
	flag.Parse()

	start := time.Now()
	api := newAPI(o.book, time.Duration(o.delay)*time.Millisecond)

	// 1) 分卷发现（DOM 由浏览器渲染，必须走一次浏览器）
	var (
		volumes []*Volume
		meta    bookMeta
		err     error
		ctx     context.Context
		outDir  string
	)
	if o.noBrowser {
		outDir, volumes, meta, err = loadLocalBook(o.out, o.book)
		if err != nil {
			fatal(fmt.Errorf("本地无缓存书目信息: %w", err))
		}
	} else {
		ctx, err = newBrowserCtx(o.visible)
		if err != nil {
			fatal(err)
		}
		defer closeBrowser(ctx)
		volumes, meta, err = discoverVolumes(ctx, o.book)
		if err != nil {
			fatal(err)
		}
		// 输出目录：<book_id>-<作者>-<书名>
		outDir = filepath.Join(o.out, bookDirName(o.book, meta.Author, meta.Title))
		if err := os.MkdirAll(outDir, 0o755); err != nil {
			fatal(err)
		}
	}

	fmt.Printf("书目: 《%s》 %s，共 %d 卷\n", meta.Title, meta.Author, len(volumes))
	for i, v := range volumes {
		fmt.Printf("  卷%d id=%s 标题=%s\n", i+1, v.ID, v.Title)
	}

	if o.onlyVol != "" {
		var keep []*Volume
		for _, v := range volumes {
			if v.ID == o.onlyVol {
				keep = append(keep, v)
			}
		}
		volumes = keep
	}
	if o.startVolume != "" {
		var keep []*Volume
		started := false
		for _, v := range volumes {
			if v.ID == o.startVolume {
				started = true
			}
			if started {
				keep = append(keep, v)
			}
		}
		volumes = keep
	}

	res := &BookResult{BookID: o.book, Title: meta.Title, Author: meta.Author, Volumes: volumes}

	// 2) 逐卷串行：先取页数，再逐页直连接口取文，每页即时落盘
	for i, v := range volumes {
		prefix := fmt.Sprintf("%02d", v.Order)
		v.Dir = filepath.Join(outDir, prefix+"_"+sanitize(v.Title))
		if err := os.MkdirAll(v.Dir, 0o755); err != nil {
			fmt.Printf("[卷%d] 建目录失败: %v\n", i+1, err)
			continue
		}

		// 页数与页目录
		total, titles, volTitle, err := volumeMeta(ctx, o.book, v, v.Dir)
		if err != nil {
			fmt.Printf("[卷%d %s] 读页数失败: %v\n", i+1, v.Title, err)
			continue
		}
		v.Total = total
		if volTitle != "" && (v.Title == "" || strings.HasPrefix(v.Title, "卷")) {
			v.Title = volTitle
			newDir := filepath.Join(outDir, prefix+"_"+sanitize(v.Title))
			if newDir != v.Dir {
				if err := os.Rename(v.Dir, newDir); err == nil {
					v.Dir = newDir
				}
			}
		}

		if err := crawlVolumeHTTP(api, v, titles, o.maxPage, i+1, start); err != nil {
			fmt.Printf("[卷%d %s] 抓取失败: %v\n", i+1, v.Title, err)
		}
		saveJSON(filepath.Join(v.Dir, "_volume.json"), v)
		saveJSON(filepath.Join(outDir, "book.json"), res)

		got := 0
		for _, p := range v.Pages {
			if p.OK {
				got++
			}
		}
		fmt.Printf("[卷%d %s] 完成 %d/%d 页，累计耗时 %s\n", i+1, v.Title, got, v.Total, time.Since(start).Round(time.Second))
	}

	// 3) 汇总落盘
	saveJSON(filepath.Join(outDir, "book.json"), res)
	if err := writeAllText(filepath.Join(outDir, sanitize(meta.Title)+"_全本.txt"), res); err != nil {
		fmt.Println("写全本文本失败:", err)
	}

	total, withText := 0, 0
	for _, v := range res.Volumes {
		total += v.Total
		for _, p := range v.Pages {
			if p.OK {
				withText++
			}
		}
	}
	fmt.Printf("全部完成：%d 卷 / %d 页 / 有文本 %d 页 / 缺失 %d 页，总耗时 %s\n",
		len(res.Volumes), total, withText, total-withText, time.Since(start).Round(time.Second))
	fmt.Println("输出目录:", outDir)
}

func fatal(err error) { fmt.Println("错误:", err); os.Exit(1) }

// ---------------- 浏览器（只用于分卷发现与页数） ----------------

func newBrowserCtx(visible bool) (context.Context, error) {
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.UserAgent(ua),
		chromedp.WindowSize(1440, 1000),
		chromedp.Flag("blink-settings", "imagesEnabled=false"),
	)
	if visible {
		opts = append(opts, chromedp.Flag("headless", false))
	} else {
		opts = append(opts, chromedp.Flag("headless", true))
	}
	found := false
	for _, p := range []string{
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"/Applications/Chromium.app/Contents/MacOS/Chromium",
	} {
		if _, err := os.Stat(p); err == nil {
			opts = append(opts, chromedp.ExecPath(p))
			found = true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("未找到 Chrome/Chromium，可加 -no-browser 使用缓存页数")
	}
	allocCtx, _ := chromedp.NewExecAllocator(context.Background(), opts...)
	ctx, _ := chromedp.NewContext(allocCtx)
	ctx, _ = context.WithTimeout(ctx, 2*time.Hour)
	return ctx, nil
}

func closeBrowser(ctx context.Context) {
	if ctx != nil {
		_ = chromedp.Cancel(ctx)
	}
}

type bookMeta struct{ Title, Author string }

func discoverVolumes(ctx context.Context, bookID string) ([]*Volume, bookMeta, error) {
	var meta bookMeta
	url := fmt.Sprintf("%s/book/%s?book_id=%s", baseURL, bookID, bookID)
	// 首次 Run 会拉起浏览器，不能带超时
	if err := chromedp.Do(ctx, chromedp.Navigate(url), chromedp.Sleep(3*time.Second)); err != nil {
		return nil, meta, err
	}
	var raw string
	for i := 0; i < 30; i++ {
		raw, _ = chromedp.Run(ctx, chromedp.Evaluate[string](fmt.Sprintf(`
(() => {
  const bid = %q;
  const links = [...document.querySelectorAll('a[href*="/volume/"]')];
  const out = []; const seen = {};
  for (const a of links) {
    const h = a.getAttribute('href') || '';
    const m = h.match(/\/book\/(\d+)\/volume\/(\d+)\/page\/(\d+)/);
    if (!m || m[1] !== bid) continue;
    if (seen[m[2]]) continue; seen[m[2]] = 1;
    const card = a.closest('.card');
    out.push({vid: m[2], card: card ? card.innerText.replace(/\s+/g, '|').slice(0, 120) : ''});
  }
  const bd = (typeof book_data !== 'undefined') ? book_data : null;
  return JSON.stringify({volumes: out, title: bd ? bd.title : '', author: bd ? bd.author : ''});
})()
`, bookID)))
		if strings.Contains(raw, `"vid"`) {
			break
		}
		time.Sleep(1 * time.Second)
	}
	var parsed struct {
		Volumes []struct {
			Vid  string `json:"vid"`
			Card string `json:"card"`
		} `json:"volumes"`
		Title  string `json:"title"`
		Author string `json:"author"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, meta, err
	}
	meta.Title, meta.Author = parsed.Title, parsed.Author
	if meta.Title == "" {
		meta.Title = "book_" + bookID
	}
	var vols []*Volume
	for i, v := range parsed.Volumes {
		t := pickTitle(v.Card)
		if t == "" {
			t = fmt.Sprintf("卷%d", i+1)
		}
		vols = append(vols, &Volume{ID: v.Vid, Order: i + 1, Title: t})
	}
	if len(vols) == 0 {
		return nil, meta, fmt.Errorf("未发现分卷(可能是单卷书或页面结构变化): %s", raw)
	}
	return vols, meta, nil
}

// volumeMeta 返回本卷页数、每页标题、卷名；优先用已有 _volume.json 缓存
func volumeMeta(ctx context.Context, bookID string, v *Volume, dir string) (int, map[int]string, string, error) {
	titles := map[int]string{}
	cachePath := filepath.Join(dir, "_volume.json")
	if data, err := os.ReadFile(cachePath); err == nil {
		var cached Volume
		if json.Unmarshal(data, &cached) == nil && cached.Total > 0 {
			for _, p := range cached.Pages {
				if p.Title != "" {
					titles[p.Page] = p.Title
				}
			}
			return cached.Total, titles, "", nil
		}
	}
	if ctx == nil {
		return 0, titles, "", fmt.Errorf("无浏览器且无缓存页数")
	}

	url := fmt.Sprintf("%s/book/%s/volume/%s/page/1?book_id=%s&volume_id=%s", baseURL, bookID, v.ID, bookID, v.ID)
	if err := chromedp.Do(ctx, chromedp.Navigate(url), chromedp.Sleep(2*time.Second)); err != nil {
		return 0, titles, "", fmt.Errorf("打开卷首页失败: %w", err)
	}
	for i := 0; i < 30; i++ {
		raw, _ := chromedp.Run(ctx, chromedp.Evaluate[string](`
(() => {
  const el = document.querySelector('#app');
  const vm = el && el.__vue_app__ && el.__vue_app__._instance.proxy;
  if (!vm) return '';
  const pages = (vm.pages_data || []).map(x => ({s: x.sort, t: x.title || ''}));
  return JSON.stringify({total: vm.page_total, title: (vm.volume && vm.volume.title) || '', pages});
})()`))
		var st struct {
			Total int    `json:"total"`
			Title string `json:"title"`
			Pages []struct {
				S int    `json:"s"`
				T string `json:"t"`
			} `json:"pages"`
		}
		if json.Unmarshal([]byte(raw), &st) == nil && st.Total > 0 {
			for _, p := range st.Pages {
				if p.T != "" {
					titles[p.S] = p.T
				}
			}
			return st.Total, titles, st.Title, nil
		}
		time.Sleep(1 * time.Second)
	}
	return 0, titles, "", fmt.Errorf("读取本卷页数失败")
}

func loadLocalVolumes(outDir string) ([]*Volume, bookMeta, error) {
	var meta bookMeta
	data, err := os.ReadFile(filepath.Join(outDir, "book.json"))
	if err != nil {
		return nil, meta, err
	}
	var res BookResult
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, meta, err
	}
	meta.Title, meta.Author = res.Title, res.Author
	return res.Volumes, meta, nil
}

// bookDirName 生成 <book_id>-<作者>-<书名> 目录名，缺一项就自动省略
func bookDirName(bookID, author, title string) string {
	parts := []string{bookID}
	if s := safeName(author); s != "" {
		parts = append(parts, s)
	}
	if s := safeName(title); s != "" {
		parts = append(parts, s)
	}
	return strings.Join(parts, "-")
}

// loadLocalBook 在输出根目录下找 <book_id> 开头的书目目录（兼容老的 <book_id> 纯 id 目录）
func loadLocalBook(root, bookID string) (string, []*Volume, bookMeta, error) {
	var meta bookMeta
	candidates := []string{filepath.Join(root, bookID)}
	entries, err := os.ReadDir(root)
	if err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			name := e.Name()
			if name == bookID || strings.HasPrefix(name, bookID+"-") {
				candidates = append(candidates, filepath.Join(root, name))
			}
		}
	}
	for _, dir := range candidates {
		if _, err := os.Stat(filepath.Join(dir, "book.json")); err != nil {
			continue
		}
		vols, m, err := loadLocalVolumes(dir)
		if err != nil {
			continue
		}
		return dir, vols, m, nil
	}
	return "", nil, meta, fmt.Errorf("在 %s 下未找到 book.json（先跑一次带浏览器的抓取生成书目）", root)
}

func pickTitle(card string) string {
	re := regexp.MustCompile("《([^》]{1,20})》")
	if m := re.FindStringSubmatch(card); len(m) == 2 {
		return m[1]
	}
	return ""
}

// ---------------- 直连接口 ----------------

type apiClient struct {
	book  string
	delay time.Duration
	http  *http.Client
}

func newAPI(book string, delay time.Duration) *apiClient {
	if delay <= 0 {
		delay = time.Second
	}
	return &apiClient{
		book:  book,
		delay: delay,
		http: &http.Client{
			Timeout:   30 * time.Second,
			Transport: &http.Transport{MaxIdleConnsPerHost: 4},
		},
	}
}

func pkcs7Pad(b []byte, size int) []byte {
	n := size - len(b)%size
	pad := bytes.Repeat([]byte{byte(n)}, n)
	return append(b, pad...)
}

func aesCBCEncrypt(plain []byte, keyHex, ivHex string) (string, error) {
	key, err := hex.DecodeString(keyHex)
	if err != nil {
		return "", err
	}
	iv, err := hex.DecodeString(ivHex)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	padded := pkcs7Pad(plain, aes.BlockSize)
	ct := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ct, padded)
	return base64.StdEncoding.EncodeToString(ct), nil
}

func aesCBCDecrypt(b64, keyHex, ivHex string) ([]byte, error) {
	key, err := hex.DecodeString(keyHex)
	if err != nil {
		return nil, err
	}
	iv, err := hex.DecodeString(ivHex)
	if err != nil {
		return nil, err
	}
	ct, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil {
		return nil, err
	}
	if len(ct)%aes.BlockSize != 0 || len(ct) == 0 {
		return nil, fmt.Errorf("密文长度异常: %d", len(ct))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	pt := make([]byte, len(ct))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(pt, ct)
	// 去 PKCS7 padding
	n := int(pt[len(pt)-1])
	if n == 0 || n > aes.BlockSize || n > len(pt) {
		return nil, fmt.Errorf("padding 异常")
	}
	return pt[:len(pt)-n], nil
}

type pageResp struct {
	BookID   int    `json:"book_id"`
	VolumeID int    `json:"volume_id"`
	Page     int    `json:"page"`
	ImageURL string `json:"image_url"`
	Text     string `json:"text"`
}

// fetchRaw 发一次请求，返回 HTTP 状态与解密后的正文
func (a *apiClient) fetchRaw(volID string, page int) (pageResp, int, error) {
	payload := fmt.Sprintf(`{"book_id":"%s","volume_id":"%s","page":%d,"text_mode":"fan"}`, a.book, volID, page)
	enc, err := aesCBCEncrypt([]byte(payload), reqKeyHex, reqIVHex)
	if err != nil {
		return pageResp{}, 0, err
	}

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	// 站点前端就是给 data 字段传一个带双引号的 base64 字符串
	if err := w.WriteField("data", `"`+enc+`"`); err != nil {
		return pageResp{}, 0, err
	}
	w.Close()

	req, err := http.NewRequest("POST", baseURL+"/api/v2/get_reader_page_content", &buf)
	if err != nil {
		return pageResp{}, 0, err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Origin", baseURL)
	req.Header.Set("Referer", fmt.Sprintf("%s/book/%s/volume/%s/page/%d?book_id=%s&volume_id=%s",
		baseURL, a.book, volID, page, a.book, volID))

	resp, err := a.http.Do(req)
	if err != nil {
		return pageResp{}, 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != 200 {
		return pageResp{}, resp.StatusCode, fmt.Errorf("HTTP %d %.60s", resp.StatusCode, truncate(string(body), 60))
	}
	var env struct {
		Data string `json:"data"`
		Msg  string `json:"message"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return pageResp{}, resp.StatusCode, fmt.Errorf("响应不是 JSON: %.80s", truncate(string(body), 80))
	}
	if env.Data == "" {
		return pageResp{}, resp.StatusCode, fmt.Errorf("响应无 data 字段: %.60s", truncate(string(body), 60))
	}
	plain, err := aesCBCDecrypt(env.Data, respKeyHex, respIVHex)
	if err != nil {
		return pageResp{}, resp.StatusCode, fmt.Errorf("解密失败: %w", err)
	}
	var pr pageResp
	if err := json.Unmarshal(plain, &pr); err != nil {
		return pageResp{}, resp.StatusCode, fmt.Errorf("正文 JSON 解析失败: %w", err)
	}
	return pr, resp.StatusCode, nil
}

// fetchPage 带限流退避的取文
func (a *apiClient) fetchPage(volID string, page int) (pageResp, int, error) {
	var lastErr error
	backoff := 2 * time.Second
	for attempt := 1; attempt <= 5; attempt++ {
		pr, status, err := a.fetchRaw(volID, page)
		if err == nil && strings.TrimSpace(pr.Text) != "" {
			return pr, attempt, nil
		}
		if err == nil {
			lastErr = fmt.Errorf("第%d页正文为空", page)
		} else {
			lastErr = err
		}
		switch {
		case status == 429 || status >= 500 || status == 0:
			fmt.Printf("      [限流/异常 status=%d] 第%d页 %.40v，%s 后重试\n", status, page, lastErr, backoff)
			time.Sleep(backoff)
			backoff *= 2
		case status == 200:
			// 200 但空文本：多半是触发风控节奏
			fmt.Printf("      [空文本] 第%d页，3s 后重试（第%d次）\n", page, attempt)
			time.Sleep(3 * time.Second)
		default:
			fmt.Printf("      [失败 status=%d] 第%d页 %.40v，%s 后重试\n", status, page, lastErr, backoff)
			time.Sleep(backoff)
			backoff *= 2
		}
		if attempt == 5 {
			break
		}
		a.sleep()
	}
	return pageResp{}, 0, lastErr
}

func (a *apiClient) sleep() { time.Sleep(a.delay) }

func crawlVolumeHTTP(api *apiClient, v *Volume, titles map[int]string, maxPage, volIdx int, startAt time.Time) error {
	end := v.Total
	if maxPage > 0 && maxPage < end {
		end = maxPage
	}
	pages := make([]Page, 0, end)

	for p := 1; p <= end; p++ {
		pagePath := filepath.Join(v.Dir, fmt.Sprintf("%04d.txt", p))
		if data, err := os.ReadFile(pagePath); err == nil && strings.TrimSpace(string(data)) != "" {
			pages = append(pages, Page{Page: p, Title: titles[p], Text: string(data), OK: true, Attempt: 0})
			continue
		}

		t0 := time.Now()
		pr, attempt, err := api.fetchPage(v.ID, p)
		if err != nil {
			fmt.Printf("  [卷%d 第%d/%d页] 失败: %v\n", volIdx, p, end, err)
			pages = append(pages, Page{Page: p, Title: titles[p], OK: false, Attempt: attempt})
			api.sleep()
			continue
		}
		// 每抓一页立刻落盘
		if werr := os.WriteFile(pagePath, []byte(pr.Text), 0o644); werr != nil {
			fmt.Printf("  [卷%d 第%d页] 写文件失败: %v\n", volIdx, p, werr)
		}
		pages = append(pages, Page{Page: p, Title: titles[p], Text: pr.Text, OK: true, Attempt: attempt})

		if p%10 == 0 || p == end {
			elapsed := time.Since(startAt)
			per := elapsed / time.Duration(maxInt(p, 1))
			fmt.Printf("  卷%d 进度 %d/%d 页  本页%dms  平均%s/页\n", volIdx, p, end,
				time.Since(t0).Milliseconds(), per.Round(time.Millisecond))
		}
		api.sleep()
	}

	sort.Slice(pages, func(i, j int) bool { return pages[i].Page < pages[j].Page })
	v.Pages = pages
	return nil
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// ---------------- 输出 ----------------

func writeAllText(path string, res *BookResult) error {
	var b strings.Builder
	fmt.Fprintf(&b, "《%s》 %s\n\n", res.Title, res.Author)
	for i, v := range res.Volumes {
		fmt.Fprintf(&b, "===== 卷%d %s (id=%s, %d页) =====\n\n", i+1, v.Title, v.ID, v.Total)
		for _, p := range v.Pages {
			if !p.OK {
				fmt.Fprintf(&b, "[第%d页] （缺失）\n", p.Page)
				continue
			}
			fmt.Fprintf(&b, "[第%d页] %s\n", p.Page, p.Text)
		}
		b.WriteString("\n")
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

func safeName(s string) string {
	s = strings.TrimSpace(s)
	s = strings.NewReplacer("/", "_", "\\", "_", "\n", "", "\r", "", "\t", "", " ", "", ":", "_", "：", "_", "*", "_", "?", "_", "\"", "_").Replace(s)
	if r := []rune(s); len(r) > 40 {
		s = string(r[:40])
	}
	return s
}

func sanitize(s string) string {
	if v := safeName(s); v != "" {
		return v
	}
	return "untitled"
}

func saveJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
