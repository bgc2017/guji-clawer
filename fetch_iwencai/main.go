package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/chromedp/chromedp"
)

const baseURL = "https://www.iwencai.com/screener/result"

// extractJS 从 .result-page-wrapper 的表格里取出表头和当前页所有行。
// 问财表格分左右两块（左侧固定列 + 右侧滚动列），这里按顺序合并。
const extractJS = `(() => {
  const clean = s => (s || "").replace(/\u00a0/g, " ").replace(/\s+/g, " ").trim();
  const root = document.querySelector(".result-page-wrapper");
  if (!root) return JSON.stringify({error: "no-result-page-wrapper"});

  const content = root.querySelector(".iwc-table-content");
  if (!content) return JSON.stringify({error: "no-iwc-table", text: clean(root.innerText).slice(0, 300)});

  const leftHead  = content.querySelector(":scope > .iwc-table-fixed > .iwc-table-header");
  const rightHead = content.querySelector(":scope > .iwc-table-scroll > .iwc-table-header");
  const leftTable = content.querySelector(":scope > .iwc-table-fixed > .iwc-table-body-outer table");
  const rightTable= content.querySelector(":scope > .iwc-table-scroll > .iwc-table-body table");

  const headNames = (h, expandSubs) => {
    if (!h) return [];
    const out = [];
    for (const li of h.querySelectorAll("li")) {
      if (li.classList.contains("checkbox-box") || li.classList.contains("nohover")) continue;
      if (li.querySelector(".tr-checkbox")) continue;
      const subs = expandSubs ? [...li.querySelectorAll(".thead-sub-box .sub-thead")] : [];
      if (subs.length) {
        for (const s of subs) { const t = clean(s.textContent); if (t) out.push(t); }
      } else {
        const t = clean(li.textContent);
        if (t) out.push(t);
      }
    }
    return out;
  };

  const cellTexts = tr => {
    const out = [];
    for (const td of tr.children) {
      if (td.tagName !== "TD") continue;
      if (td.classList.contains("checkbox-box")) continue;
      if (td.querySelector(":scope > .td-cell-box.no-padding")) continue; // 序号列
      out.push(clean(td.innerText));
    }
    return out;
  };

  const rows = tbl => {
    if (!tbl) return [];
    return [...tbl.querySelectorAll(":scope > tbody > tr")].map(cellTexts);
  };

  const headers = [...headNames(leftHead, false), ...headNames(rightHead, true)];
  const leftRows = rows(leftTable), rightRows = rows(rightTable);
  const n = Math.max(leftRows.length, rightRows.length);
  const merged = [];
  for (let i = 0; i < n; i++) {
    merged.push([...(leftRows[i] || []), ...(rightRows[i] || [])]);
  }

  // 左侧固定列是右侧滚动表格的重复覆盖层：右侧表格通常已经包含全部列，
  // 这时直接用右侧行，否则把左右两半拼起来。
  let data = merged;
  if (rightRows.length && rightRows[0].length === headers.length) {
    data = rightRows;
  } else if (merged.length && merged[0].length === headers.length) {
    data = merged;
  } else if (rightRows.length && rightRows[0].length > (merged[0] || []).length) {
    data = rightRows;
  }
  return JSON.stringify({headers, rows: data});
})()`

// tableReadyJS 返回当前是否已经有数据行。
const tableReadyJS = `(() => {
  const root = document.querySelector(".result-page-wrapper");
  if (!root) return JSON.stringify({root: false, rows: 0});
  const content = root.querySelector(".iwc-table-content");
  const rows = content ? content.querySelectorAll(".iwc-table-body tbody tr").length : 0;
  const text = (root.innerText || "").replace(/\s+/g, " ").trim().slice(0, 200);
  return JSON.stringify({root: true, table: !!content, rows, text});
})()`

// nextPageJS 点击下一页；没有下一页时返回 false。
const nextPageJS = `(() => {
  const n = document.querySelector(".pcwencai-pagination-wrap .next-link");
  if (!n || n.classList.contains("disabled")) return false;
  n.click();
  return true;
})()`

// setPageSizeJS 把每页条数切到 100，减少翻页次数。
const openPageSizeJS = `(() => {
  const box = document.querySelector(".pcwencai-pagination-wrap .drop-down-box");
  if (!box) return false;
  box.click();
  return true;
})()`

const pickPageSizeJS = `(() => {
  const lis = [...document.querySelectorAll(".pcwencai-pagination-wrap .drop-down-box li")];
  const li = lis.find(x => x.textContent.includes("100条/页"));
  if (!li) return false;
  li.click();
  return true;
})()`

type pageData struct {
	Headers []string   `json:"headers"`
	Rows    [][]string `json:"rows"`
	Error   string     `json:"error"`
	Text    string     `json:"text"`
}

type options struct {
	query     string
	filter    string
	queryType string
	visible   bool
	profile   string
	timeout   time.Duration
	save      string
	allPages  bool
}

func main() {
	var o options
	flag.StringVar(&o.query, "w", "", "查询问句，例如：二连板")
	flag.StringVar(&o.filter, "filter", "股票代码", "要输出的列，逗号分隔，例如：股票代码,股票简称,最新价")
	flag.StringVar(&o.queryType, "querytype", "stock", "querytype，默认 stock")
	flag.BoolVar(&o.visible, "visible", false, "显示浏览器窗口（首次登录问财时使用）")
	flag.StringVar(&o.profile, "profile", defaultProfileDir(), "Chrome 用户数据目录，登录状态保存在这里")
	flag.DurationVar(&o.timeout, "timeout", 120*time.Second, "等待页面出结果的超时时间")
	flag.StringVar(&o.save, "save", "", "调试：把最终页面 HTML 保存到该文件")
	flag.BoolVar(&o.allPages, "all", true, "抓取全部分页")
	flag.Parse()

	if strings.TrimSpace(o.query) == "" {
		fmt.Fprintln(os.Stderr, "用法: wencai -w \"二连板\" [-filter 股票代码,股票简称,最新价] [-visible]")
		os.Exit(2)
	}
	if err := os.MkdirAll(o.profile, 0o700); err != nil {
		fatal(fmt.Errorf("创建浏览器配置目录失败: %w", err))
	}

	cols := splitFilter(o.filter)
	pages, headers, err := crawl(o, cols)
	if err != nil {
		fatal(err)
	}

	fmt.Printf("# 可用列（-filter 可选）: %s\n", strings.Join(headers, "\t"))
	for _, row := range pages {
		if len(row) == 0 {
			continue
		}
		out := make([]string, len(cols))
		for i := range cols {
			out[i] = row[i]
		}
		fmt.Println(strings.Join(out, "\t"))
	}
}

func crawl(o options, cols []string) ([][]string, []string, error) {
	ctx, cancel := newBrowser(o)
	defer cancel()

	target := baseURL + "?" + url.Values{
		"w":         {o.query},
		"querytype": {o.queryType},
	}.Encode()

	if err := chromedp.Do(ctx,
		chromedp.Navigate(target),
		chromedp.Sleep(2*time.Second),
	); err != nil {
		return nil, nil, fmt.Errorf("打开页面失败: %w", err)
	}

	var headers []string
	if err := waitTable(ctx, o.timeout); err != nil {
		savePage(ctx, o.save)
		return nil, nil, err
	}

	// 尽量切到 100 条/页，减少翻页。
	oldRows := currentRowCount(ctx)
	if ok, _ := chromedp.Run(ctx, chromedp.Evaluate[bool](openPageSizeJS)); ok {
		time.Sleep(300 * time.Millisecond)
		if ok2, _ := chromedp.Run(ctx, chromedp.Evaluate[bool](pickPageSizeJS)); ok2 {
			_ = waitRowCountChange(ctx, oldRows, 20*time.Second)
		}
	}

	var all [][]string
	for page := 1; ; page++ {
		raw, err := chromedp.Run(ctx, chromedp.Evaluate[string](extractJS))
		if err != nil {
			return nil, nil, fmt.Errorf("读取表格失败: %w", err)
		}
		var pd pageData
		if err := json.Unmarshal([]byte(raw), &pd); err != nil {
			return nil, nil, fmt.Errorf("解析表格数据失败: %w", err)
		}
		if pd.Error != "" {
			savePage(ctx, o.save)
			return nil, nil, fmt.Errorf("页面里没有选股表格（%s）", pd.Error)
		}
		if len(headers) == 0 {
			headers = pd.Headers
		}
		rows, err := projectRows(pd.Rows, headers, cols)
		if err != nil {
			return nil, nil, err
		}
		all = append(all, rows...)

		if !o.allPages || page >= 200 {
			break
		}
		oldFirst := ""
		if len(pd.Rows) > 0 && len(pd.Rows[0]) > 0 {
			oldFirst = pd.Rows[0][0]
		}
		clicked, err := chromedp.Run(ctx, chromedp.Evaluate[bool](nextPageJS))
		if err != nil || !clicked {
			break
		}
		if err := waitNextPage(ctx, oldFirst, 15*time.Second); err != nil {
			break
		}
	}
	savePage(ctx, o.save)
	return all, headers, nil
}

func newBrowser(o options) (context.Context, context.CancelFunc) {
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.WindowSize(1440, 1000),
		chromedp.UserDataDir(o.profile),
		chromedp.Flag("lang", "zh-CN"),
		// 问财的 chameleon 会检测自动化环境。带 automation 标志时它生成的
		// hexin-v 令牌会被 upass 登录页判为非法，返回 "Nginx forbidden."。
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
		chromedp.Flag("enable-automation", false),
	)
	if o.visible {
		opts = append(opts, chromedp.VisibleWindow)
	}
	if p := chromePath(); p != "" {
		opts = append(opts, chromedp.ExecPath(p))
		// 用真实安装的 Chrome 版本构造 UA，避免 UA 与 Client Hints 版本不一致。
		if ua := userAgent(p); ua != "" {
			opts = append(opts, chromedp.UserAgent(ua))
		}
	}

	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), opts...)
	ctx, cancelCtx := chromedp.NewContext(allocCtx)
	ctx, cancelTimeout := context.WithTimeout(ctx, o.timeout+2*time.Minute)
	return ctx, func() {
		cancelTimeout()
		cancelCtx()
		cancelAlloc()
	}
}

func waitTable(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var last string
	for {
		var state struct {
			Root  bool   `json:"root"`
			Table bool   `json:"table"`
			Rows  int    `json:"rows"`
			Text  string `json:"text"`
		}
		if raw, err := chromedp.Run(ctx, chromedp.Evaluate[string](tableReadyJS)); err == nil {
			if json.Unmarshal([]byte(raw), &state) == nil {
				last = state.Text
				if state.Rows > 0 {
					return nil
				}
			}
		}
		if time.Now().After(deadline) {
			return errors.New("等待结果超时" + loginHint(last))
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func currentRowCount(ctx context.Context) int {
	raw, err := chromedp.Run(ctx, chromedp.Evaluate[string](tableReadyJS))
	if err != nil {
		return 0
	}
	var state struct {
		Rows int `json:"rows"`
	}
	if json.Unmarshal([]byte(raw), &state) != nil {
		return 0
	}
	return state.Rows
}

func waitRowCountChange(ctx context.Context, old int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		n := currentRowCount(ctx)
		if n > 0 && n != old {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("等待每页条数切换超时")
		}
		time.Sleep(300 * time.Millisecond)
	}
}

func waitNextPage(ctx context.Context, oldFirst string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		raw, err := chromedp.Run(ctx, chromedp.Evaluate[string](extractJS))
		if err == nil {
			var pd pageData
			if json.Unmarshal([]byte(raw), &pd) == nil && len(pd.Rows) > 0 && len(pd.Rows[0]) > 0 {
				if oldFirst == "" || pd.Rows[0][0] != oldFirst {
					return nil
				}
			}
		}
		if time.Now().After(deadline) {
			return errors.New("翻页超时")
		}
		time.Sleep(300 * time.Millisecond)
	}
}

func projectRows(rows [][]string, headers, cols []string) ([][]string, error) {
	idx := make([]int, len(cols))
	for i, name := range cols {
		j := findColumn(headers, name)
		if j < 0 {
			return nil, fmt.Errorf("找不到列 %q；当前可选列: %s", name, strings.Join(headers, ", "))
		}
		idx[i] = j
	}
	out := make([][]string, len(rows))
	for r, row := range rows {
		out[r] = make([]string, len(cols))
		for i, j := range idx {
			if j < len(row) {
				out[r][i] = row[j]
			}
		}
	}
	return out, nil
}

func findColumn(headers []string, name string) int {
	name = strings.TrimSpace(name)
	for i, h := range headers {
		if h == name {
			return i
		}
	}
	for i, h := range headers {
		if strings.Contains(h, name) {
			return i
		}
	}
	for i, h := range headers {
		if strings.Contains(name, h) {
			return i
		}
	}
	return -1
}

func splitFilter(s string) []string {
	var out []string
	for _, p := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == '，' }) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		out = []string{"股票代码"}
	}
	return out
}

func savePage(ctx context.Context, path string) {
	if path == "" {
		return
	}
	html, err := chromedp.Run(ctx, chromedp.Evaluate[string](`document.documentElement.outerHTML`))
	if err == nil {
		_ = os.WriteFile(path, []byte(html), 0o644)
	}
}

func loginHint(text string) string {
	hint := ""
	if text != "" {
		hint = "\n页面提示: " + text
	}
	return hint + "\n如果页面要求登录，请先运行：wencai -visible -w \"你的问句\"，在弹出的浏览器里登录一次（登录状态会保存在 -profile 目录）。"
}

func defaultProfileDir() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return ".wencai-chrome-profile"
	}
	return filepath.Join(dir, "wencai-crawler", "chrome-profile")
}

// userAgent 读取 Chrome 实际版本，拼出与浏览器一致的普通 UA（不暴露 HeadlessChrome）。
func userAgent(chrome string) string {
	out, err := exec.Command(chrome, "--version").Output()
	if err != nil {
		return ""
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return ""
	}
	ver := fields[len(fields)-1]
	if i := strings.IndexByte(ver, '.'); i > 0 {
		ver = ver[:i]
	}
	platform := "X11; Linux x86_64"
	switch runtime.GOOS {
	case "darwin":
		platform = "Macintosh; Intel Mac OS X 10_15_7"
	case "windows":
		platform = "Windows NT 10.0; Win64; x64"
	}
	return fmt.Sprintf("Mozilla/5.0 (%s) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/%s.0.0.0 Safari/537.36", platform, ver)
}

func chromePath() string {
	for _, p := range []string{
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"/Applications/Chromium.app/Contents/MacOS/Chromium",
		"/usr/bin/google-chrome",
		"/usr/bin/chromium",
		"/usr/bin/chromium-browser",
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "错误:", err)
	os.Exit(1)
}
