package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/chromedp/chromedp"
)

// 与主程序一致的 UA：无头浏览器必须像一个普通桌面浏览器，否则站点返回 Data exception。
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

type launchOptions struct {
	visible   bool
	chrome    string
	ua        string
	proxy     string
	keepImage bool
	verbose   bool
}

func launchChrome(o launchOptions) (context.Context, func(), error) {
	path, err := findChrome(o.chrome)
	if err != nil {
		return nil, nil, err
	}
	ua := o.ua
	if ua == "" {
		ua = defaultUserAgent()
	}

	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(path),
		chromedp.UserAgent(ua),
		chromedp.WindowSize(1440, 1200),
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
		fmt.Fprintf(os.Stderr, "浏览器: %s\n", path)
	}
	if o.visible {
		opts = append(opts, chromedp.VisibleWindow)
	}
	if !o.keepImage {
		opts = append(opts, chromedp.Flag("blink-settings", "imagesEnabled=false"))
	}
	if runtime.GOOS == "linux" {
		opts = append(opts, chromedp.Flag("no-sandbox", true))
	}

	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), opts...)
	ctx, cancel := chromedp.NewContext(allocCtx)
	if err := chromedp.Do(ctx, chromedp.Navigate("about:blank")); err != nil {
		cancel()
		allocCancel()
		return nil, nil, fmt.Errorf("启动浏览器失败: %w", err)
	}
	cleanup := func() {
		cancel()
		allocCancel()
	}
	return ctx, cleanup, nil
}
