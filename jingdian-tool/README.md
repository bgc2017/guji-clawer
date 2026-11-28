# jingdian —— 籍合网（中华经典古籍库）搜索 API 工具

把 `https://jingdian.ancientbooks.cn/6/search?txt=…` 这个前端搜索接口改造成可直接调用的 JSON API，
并且**还原站点的字体混淆**，返回可读的检索片段（不依赖 OCR，也不依赖登录）。

## 原理（一句话）

站点把 snippet 里的字换成私用区码位，再用动态生成的 woff 把字形摆回去；而这些字形本身取自官方公开的
「中华书局宋体」00/02/15/16 四个平面字库。所以只要把每个混淆字形与官方字库做轮廓比对（点数 → 包围盒 → 逐点 L1 距离），
就能精确还原成真实字符。

关键细节（都是踩过的坑）：

- 请求必须带 `X-Requested-With: XMLHttpRequest`、`siteid: 1`、`Origin`/`Referer` 和浏览器 UA；`modelId` 用 `5`（全文），`1` 会返回 total=0。
- **关键词不在平铺字段里**，而是 `conditionJson`：`[{"column":"txt","searchTxt":"黄鹤楼","relation":"and"}]`。漏了它服务端按「无条件」处理，直接返回全库第一页（4,152,175 条，任何词都一样）——这是最容易踩的坑。
- 返回的 snippet 是带高亮的 HTML（`<em class="highlight">词</em>`），还原后要去标签。
- 字体地址 `https://jingdian.ancientbooks.cn/font/{folder}/{md5}/{pathArray[i]}.woff`，每次响应 md5 都会轮换，因此**必须运行时下载解码**，不能写死映射表。
- 同组 4 个字库里，只有 1 个真正带字形，其余是 64 个映射到空字形的占位，而且排在字体栈前面。所以不能「第一个命中就用」，要遍历整组取距离最小的匹配。
- 这些空字形同时被当作**不可见填充字符**塞进 snippet 里（复制出来是乱码，人看着是空的）。解码时若某个码位在所有字库里都画不出东西，直接丢弃，文本才对得上。
- 解码后还要把 `﹁﹂﹃﹄﹐﹑﹒﹔﹕﹝﹞` 这类竖排/呈现形式还原成 `“”‘’，、。；：【】`。

## 准备字库

把官方的四个平面字库放进 `fonts/`（文件名随意，`.ttf/.otf/.ttc` 都会被索引）：

```
fonts/00.TTF   中华书局宋体00平面
fonts/02.TTF   中华书局宋体02平面
fonts/15.TTF   中华书局宋体15平面
fonts/16.TTF   中华书局字休16平面
```

启动时会把它们建成轮廓索引（约 1~2 秒，十几万字形）。只放 00 平面也能跑，但生僻字会有漏。

## 用法

命令行单次查询：

```bash
go build -o jingdian .
go build -o jingdian .                # 默认：纯 HTTP，不含浏览器代码
go build -tags browser -o jingdian-browser .   # 需要兜底时

./jingdian -q '黄鹤楼' -num 50        # 打印 JSON
./jingdian-browser -q '黄鹤楼' -num 3 -browser # 走真实 Chrome，抗风控兜底
```

起 HTTP 服务：

```bash
./jingdian -addr 127.0.0.1:8088
curl 'http://127.0.0.1:8088/search?q=黄鹤楼&num=50'
curl 'http://127.0.0.1:8088/search?q=黄鹤楼&page=2&num=50&lib=6'
curl 'http://127.0.0.1:8088/health'
```

参数：`q` 关键词（必填）、`num` 返回条数（默认 50，上限 50）、`page` 起始页（每页 50）、`lib` 库 ID（URL 里的 `/6/`，默认 6）。

返回：

```json
{
  "query": "黄鹤楼",
  "libId": 6,
  "page": 1,
  "pageSize": 50,
  "total": 736,
  "returned": 50,
  "elapsedMs": 900,
  "items": [
    {
      "contentId": 4807723,
      "bookId": 1554113,
      "title": "版权页",
      "source": "十三經注疏 清嘉慶刊本 > 版权页",
      "catalogue": "十三經注疏 清嘉慶刊本/［清］阮元 校刻 /中華書局/2009年10月/經部/總類",
      "author": "［清］阮元 校刻",
      "category": "經部/總類",
      "publisher": "中華書局",
      "pubDate": "2009年10月",
      "snippet": "圖書在版編目（CIP）數據十三經注疏：清嘉",
      "coverUrl": "https://pub.ancientbooks.cn/publish/u/www/202410/1618105881nj.jpg",
      "hasFullText": false
    }
  ]
}
```

`title/source/catalogue/author` 本来就是明文，直接透传；只有 `snippet`（原 `description`）是混淆的，由字体解码还原。

`snippet` 是纯文本，`highlights` 保留原网页的关键词高亮位置（`start`/`end` 是**字符下标**，不是字节偏移），
前端可以直接拿来重建 `<em>` 标记：

```json
"snippet": "西江，索诗归，祝仲复年伯》《忆书五首》《黄鹤楼》《华岩洞》…",
"highlights": [{ "text": "黄鹤楼", "start": 20, "end": 23 }]
```
`-browser` 时接口路径不变，只是搜索请求改在真实 Chrome 标签页里发出（`fetch` + `credentials: include`），
用于 HTTP 直连被网关拦截（验证码 / 限流）的情况。

## 实测结果

- `黄鹤楼` 736 条 / `论语` 10,393 条 / `苏轼` 8,728 条 / `红楼梦` 534 条，取 50 条约 2.3~2.9 s（含字体下载与解码，字库按 md5 缓存到 `data/`）。
- 5 个关键词 × 50 条共 250 条：每条都命中关键词（250/250），未还原字共 3 个（0.002%），为个别异体。
- 翻页正常（第 2 页 contentId 与第 1 页不同）。

## 要不要浏览器（结论：不需要）

搜索链路用纯 `net/http` 就已打通，浏览器只作为可选兜底：

| 方案 | 50 条耗时 | 依赖 | 备注 |
|---|---|---|---|
| 纯 HTTP（默认） | 1.2 s | 无 Chrome、无 GUI、无显示器 | 直接 POST `/v1/search`，服务端只校验请求头与 SESSION cookie |
| chromedp 兜底 | 5.2 s | 真实 Chrome 进程 | 同一请求改在标签页里 `fetch`，抗网关风控 |

所以：

- 默认构建**根本不编译 chromedp**（`internal/site/browser.go` 带 `//go:build browser`），`go build` 出来的二进制不包含浏览器相关代码，可在无头容器/服务器直接跑。
- 需要兜底时再 `go build -tags browser`；运行时加 `-browser` 才会走 Chrome。
- 什么时候真需要浏览器：站点将来加 JS 挑战（如瑞数/极验）或请求签名，那时才绕不开；目前只在响应头里校验 UA、`X-Requested-With`、`siteid`、`Origin/Referer`，`-browser` 目前是备用而不是必需。
- 浏览器方案的成本：每个进程约 300 MB 内存、需装 Chrome、容器里得加 `--no-sandbox`、并发受限于进程数；纯 HTTP 走连接复用，几十并发没问题（配合 `data/` 的字库缓存，重复查询几乎不额外下载字体）。

## 目录

```
main.go                     CLI + HTTP 服务
internal/site/client.go     搜索接口、字体下载（cookie jar、必要请求头）
internal/site/browser.go    chromedp 兜底（仅在 -tags browser 时编译）
internal/site/browser_stub.go 无浏览器构建下的空实现
internal/glyph/index.go     官方字库轮廓索引与匹配（点数/包围盒/L1 距离 + 码位优先级）
internal/glyph/fontset.go   混淆字体集解码器（跨字库挑最佳匹配、空字形填充字符丢弃、标点归一）
internal/woff/woff.go       WOFF1 → sfnt 解码（官方字库是裸 sfnt，混淆字库是 WOFF1）
fonts/                      官方中华书局宋体四平面（需自行放入）
data/                       下载的混淆 woff 缓存
```

## 已知限制

- WOFF2 未支持（站点目前下发的是 WOFF1）。
- 极个别异体字可能还原不出，会原样保留码位（不影响阅读上下文）。
- 只是搜索接口；正文接口 `GET /v1/articles/{contentId}` 里的 `txt` 同样被混淆，需要登录权限，本工具未涉及。
