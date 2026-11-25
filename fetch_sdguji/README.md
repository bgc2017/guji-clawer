# shidianguji 图书下载器

用 Go + [chromedp](https://github.com/chromedp/chromedp) 驱动真实 Chrome，把识典古籍（shidianguji.com）上指定图书的**整本正文**抓成本地纯文本。全程模拟真人浏览器，不碰站点私有接口签名。

## 编译

```bash
go build -o sdguji .
```

要求本机装有 Chrome（自动探测 macOS / Linux / Windows 常见路径），也可以用环境变量指定：

```bash
export SDG_CHROME="/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
```

## 在 Windows 上运行

`dist/sdguji-windows-amd64-*.zip` 是交叉编译好的单文件程序，字典已 embed，解压后不依赖 Go 或其它运行库：

```bat
sdguji.exe -book https://www.shidianguji.com/book/CADAL01023318 -out D:\guji
```

Windows 与 macOS 的差异都已处理掉，换机器不用改代码也不用重编译：

- **浏览器探测**：Windows 下依次找 `Program Files`、`Program Files (x86)`、`LocalAppData` 里的 Chrome，再找系统自带的 Edge（同为 Chromium 内核）；也可用 `-chrome "C:\Program Files\Google\Chrome\Application\chrome.exe"` 指定。
- **控制台编码**：启动时把代码页切到 UTF-8（65001），cmd / PowerShell 里中文不乱码。
- **UA**：按当前系统生成（Windows 用 `Windows NT 10.0`），不再把 UA 写死成 macOS。
- **文件名**：已过滤 `: * ? " < > | / \` 等 Windows 非法字符、控制字符和结尾的点，并避开 `CON`/`PRN`/`COM1` 等保留名；章节名截断到 60 字，避免超长路径。
- **代理**：`-proxy http://127.0.0.1:7890` 直接传给浏览器。

重新打包（在 macOS/Linux 上即可，交叉编译，无需 Windows 机器）：

```bash
./package.sh 20261125
# 产出 dist/sdguji-windows-amd64-<ver>.zip、dist/sdguji-windows-arm64-<ver>.zip
```

只支持 64 位 Windows（amd64 / arm64）；exe 未签名，首次运行 SmartScreen 会提示，需手动允许。

## 用法

```bash
# 整本下载（最常用）
./sdguji -book https://www.shidianguji.com/book/CADAL01023318

# 也可以只给 ID
./sdguji -book CADAL01023318

# 先看目录，不下载
./sdguji -book CADAL01023318 -list

# 只抓指定章（按标题或章节 ID，可逗号分隔）
./sdguji -book CADAL01023318 -only 卷一,醜女賦

# 从某章开始抓，最多 5 章
./sdguji -book CADAL01023318 -from 卷五 -max 5

# 断点续抓：已有非空文件的章节自动跳过；-force 强制重抓
./sdguji -book CADAL01023318 -force

# 调试：显示浏览器窗口、保留原始段落 JSON
./sdguji -book CADAL01023318 -visible -raw -v

# 正文字形：默认标准繁体，可切机器简体或站点原文
./sdguji -book HY7246 -text simp
./sdguji -book HY7246 -text orig
```

## 参数

| 参数 | 默认 | 说明 |
| --- | --- | --- |
| `-book` | 必填 | 图书 URL 或裸 ID，支持 `/zh/`、`/ens/`、`/bo/` 等语言前缀 |
| `-out` | `books` | 输出根目录 |
| `-chrome` | 自动探测 | 浏览器可执行文件路径；不填则按系统找 Chrome/Edge |
| `-ua` | 按系统生成 | 自定义 User-Agent |
| `-proxy` | 空 | 浏览器代理，如 `http://127.0.0.1:7890` |
| `-lang` | 从 URL 推断，否则 `zh` | 站点语言路径 |
| `-visible` | 关 | 显示浏览器窗口（遇验证码或排查风控时用） |
| `-images` | 关 | 加载图片，默认关闭以加速（正文是文字，不依赖图片） |
| `-delay` | `800` | 每章之间的随机停顿毫秒数（0.75–1.25 倍抖动） |
| `-timeout` | `90` | 单页操作超时秒数 |
| `-force` | 关 | 忽略本地已有文件，强制重抓 |
| `-only` | 空 | 只抓匹配的章：完整标题、标题后缀或章节 ID，逗号分隔 |
| `-from` | 空 | 从匹配的章开始往后抓 |
| `-max` | `0` | 最多抓多少章（0 为不限） |
| `-list` | 关 | 只打印目录和页范围 |
| `-text` | `trad` | 正文字形：`trad` 标准繁体 / `simp` 机器简体 / `orig` 站点原文（异体字原样） |
| `-raw` | 关 | 每章另存 `raw/NNN_标题.json`（含段落原始字段） |
| `-v` | 关 | 输出调试信息 |

## 输出结构

```
books/CADAL01023318-皇朝駢文類苑/
├── 001_皇朝駢文類苑卷首.txt      # 每章一个文件：标题 + 页范围 + 正文
├── 002_皇朝駢文類苑目錄.txt
├── 003_皇朝駢文類苑卷一.txt
├── ...
├── book.json                     # 书目信息 + 每章状态/页范围/字数/耗时
└── 皇朝駢文類苑_全本.txt          # 全书合并版
```

`book.json` 里每章都有 `status`（`done` / `cached` / `failed` / `empty`）和 `note`（失败原因或截断提示），可以据此只补抓失败章。`text_mode` 记录本次（以及每章文件）用的字形模式；换模式重跑只重抓模式不符的章节，不会把旧字形文件当成已缓存跳过。

文件名沿用目录里的原始标题（保证续抓稳定、不会因字形变化重名），文件内的章节标题会随 `-text` 一起转换。

## 正文字形（异体字）

站点把正文分三档字形，阅读器右上角可切换：

- **原文**：OCR 原始字形，异体字保留（`徳`、`𠑽`、`𢙣`）。
- **标准繁体**：异体字归一（`德`、`充`、`惡`），缺字仍保留原字形。
- **机器简体**：再转简体（`德`、`充`、`恶`）。

实测确认：`__is_simplified`（0 繁体 / 1 简体 / 2 原文）这个 cookie **只影响阅读器显示和页面元信息**，章节页 SSR 响应里的 `paragraphList` 永远是原书字形。也就是说光给请求加 cookie 拿不到转换后的正文；转换是浏览器端用前端 bundle 里的字典做的。

脚本的处理方式：把站点自带的这两张字典抄进 `data/`，在本地复现同样的转换，速度和稳定性都比渲染 DOM 好，也不会被分页影响。对照验证过：同一章用浏览器设 cookie 渲染出来的正文，与脚本 `-text trad` 的输出逐字一致（站点自己也保留 `逹`、`𤍠` 这类字典外的字）。

- `data/s2t.json`：简/异体 → 标准繁体，`-text trad` 使用
- `data/t2s.json`：繁/异体 → 简体，含 284 条词组（如「一目瞭然」→「一目了然」），`-text simp` 使用

字典提取自站点前端 bundle（chromedp 渲染页面时加载的 JS chunk）。站点若改版更新了转换表，`-text` 结果可能与网页显示有几字出入；需要的话重新从 bundle 里抽取这两个 JSON 即可，格式不用变。

## 实现要点

1. **真实浏览器**：`chromedp` 启动本机 Chrome；headless 模式下伪装正常 UA、关掉 `AutomationControlled`，否则页面会返回 “Data exception”。默认禁用图片和缓存。
2. **目录**：打开 `/book/{id}`，等前端把目录树拉回来，取 `.semi-tree-option-list` 里第一个章节链接作为全书第一章；目录拿不到时尝试点“开始阅读”入口，再退回重载。
3. **整章正文**：章节页是 SSR 页面，`paragraphList` 就在 HTML 内联的 `window._ROUTER_DATA` 里，**一次请求就能拿到整章**，不必逐页翻。关键是不能读页面里的 `window`——前端 hydration 会把那份数据换成空壳，所以改用 CDP `network.GetResponseBody` 取回**导航响应原文**，再扫描括号截出 JSON 解析。
4. **正文还原**：`content` 是含 `lines` 的 JSON；`lineType` 1 为正文、2 为小字注（输出时加全角括号）、3 为分隔（丢弃），加密段落（`contentEncryptType != 0`）跳过。
5. **父章去重**：有子章的父章只保留 `chapterId` 等于自身的段落，避免和子章内容重复；正文开头与标题重复的那一段会被去掉。
6. **字形转换**：正文按 `-text` 用内置字典转换（词组优先、逐字兜底），同时把 `__is_simplified` cookie 同步给浏览器，让 DOM 兜底与输出保持一致。
7. **兜底**：SSR 解析失败时，按 `.vik-toolbox-btn[type="page-next"]` 逐页点“下一页”，取 DOM 中段落最多的快照；仍失败则标记 `failed` 并继续下一章，最后汇总退出码为 1。

## 已知限制

- 正文来自站点的 OCR 结果；`-text trad` / `-text simp` 只做字形归一，不校勘讹字、不改动双行小字位置（需要校勘可配合本目录的 `guji-proofread` 技能）。
- 只抓文字，不下载书影图片；`-images` 仅让浏览器加载图片，没有落盘逻辑。
- 站点有频率限制，默认每章间隔约 0.8 秒；一次抓几百章建议不要并发，遇到验证码用 `-visible` 手动过一次。
- 章节页结构一旦改版（`window._ROUTER_DATA` 标记或字段名变化），需要同步调整 `parse.go`。
