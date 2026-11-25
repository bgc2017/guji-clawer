# wencai 爬虫

用 chromedp 驱动真实 Chrome，打开问财选股结果页，从 `.result-page-wrapper` 的表格里取数据。

## 用法

```bash
# 首次使用：先打开可见浏览器登录一次（登录态保存在 -profile 目录）
go run . -visible -w "二连板"

# 之后可以无头运行
go run . -w "二连板"

# 输出多列（tab 分隔）
go run . -w "2026-11-18，二板，主板" -filter "股票代码,股票简称,最新价"
```

第一行会打印当前查询结果的全部列名，`-filter` 里的列名就按这一行动态匹配：

```text
# 可用列（-filter 可选）: 股票代码	股票简称	最新价	涨跌幅(%)	...
000530	主板
...
```

参数：

- `-w`：问句（必填）
- `-filter`：要输出的列，逗号分隔，默认 `股票代码`
- `-querytype`：默认 `stock`
- `-visible`：显示浏览器窗口，首次登录用
- `-profile`：Chrome 用户数据目录，默认 `~/Library/Caches/wencai-crawler/chrome-profile`
- `-timeout`：等待出结果的超时，默认 120s
- `-all`：是否翻页抓全，默认 true
- `-save`：调试用，把最终页面 HTML 存到文件

URL 不需要自己加 `sign`，打开页面后由问财前端自动补上。

## 说明

问财的选股接口要求登录。没有登录态时页面只会显示空结果，程序会提示先用 `-visible` 登录。

登录页出现 `Nginx forbidden.` 是问财 chameleon 反自动化导致的：自动化标志会让它生成的
`hexin-v` 令牌被 upass 判为非法。程序已经关闭 automation 标志
（`--disable-blink-features=AutomationControlled`），不再触发该问题。
