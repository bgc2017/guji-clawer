# sdguji-search — 识典古籍「搜书籍」筛选导出

独立脚本，不改动主下载器的任何源码。用途：按关键词搜索书籍，按「四部分类 + 朝代」筛选，
排除来源含「四库 / 四部」的书，只保留「精校 / 粗校」的书，输出 `ID  书名  朝代  作者`（Tab 分隔）。

## 为什么必须开浏览器

搜索接口 `POST /api/ancientlib/read/search/book/v1` 的 query 带字节签名
（`verifyFp / fp / msToken / a_bogus`）。直接 curl、或者把抓到的 URL 改 body 回放，
都会返回 `{"errorCode":40001}`——签名与请求体绑定，改一个字就失效。
在页面里用 `fetch` / `XMLHttpRequest` 自发也不行，签名由站点自己的请求封装生成。

所以脚本走的是「完整模拟浏览器」：打开搜索页 → 点「搜书籍」→ 在筛选面板勾选分类与朝代 → 翻页，
用 CDP 监听页面自己发出的每个搜索请求，把请求体（看筛选参数）和响应体（看书籍数据）一起抓下来。

## 编译

```bash
go build -o sdguji-search ./search                 # 当前系统
GOOS=windows GOARCH=amd64 go build -o sdguji-search.exe ./search
GOOS=windows GOARCH=arm64 go build -o sdguji-search.exe ./search
```

`package.sh` 也会一起把 `sdguji-search.exe` 打进 Windows zip。

## 用法

```bash
# 诗集 + 清代 + 集部-别集类（此组合站点给 489 条）
./sdguji-search -q 诗集 -dynasty 清 -cate 集部-别集类 -out 诗集_清_集部别集类.txt -v

# 只要分类，不要朝代
./sdguji-search -q 杜甫 -cate 集部-别集类

# 多个朝代 / 多个分类用逗号分隔
./sdguji-search -q 诗集 -dynasty 清,明 -cate 集部-别集类,集部-总集类
```

参数：

| 参数 | 说明 |
| --- | --- |
| `-q` | 搜索关键词（也可作为第一个位置参数） |
| `-dynasty` | 朝代筛选，如 `清`；逗号分隔多选；不传则不筛 |
| `-cate` | 四部分类，`-` 表示层级，如 `集部-别集类`、`集部-别集类-清代` |
| `-status` | 保留的校对状态，默认 `精校,粗校` |
| `-exclude` | 来源排除词，默认 `四库,四庫,四部`（覆盖「四部叢刊」「四庫全書本」等）；传空串关闭 |
| `-out` | 输出文件；不填打印到 stdout |
| `-header` | 额外输出一行表头 |
| `-max-pages` | 翻页上限（保护），默认 300 |
| `-delay` | 每页之间的额外等待，默认 600ms |
| `-v` | 打印进度、校对状态分布、过滤统计 |
| `-dump` | 把抓到的原始书籍 JSON 存一份，便于排查 |
| `-visible` | 显示浏览器窗口 |
| `-chrome` | 指定 Chrome/Edge 路径（Windows 会自动找 Chrome，再找 Edge） |
| `-proxy` / `-ua` | 代理 / 自定义 UA |
| `-timeout` | 整体超时，默认 20 分钟 |

## 输出的字段来源

- `ID`：`book.bookId`（可直接拼 `https://www.shidianguji.com/book/{ID}`）
- `书名`：`book.bookName`
- `朝代`：`book.dynastyCategoryName`（空则退回第一作者的 `dynastyName`）
- `作者`：`book.authors[].persName`，多人用 `、` 连接

## 校对状态枚举（实测，对应卡片徽标）

| checkType | 卡片显示 |
| --- | --- |
| 1 | 粗校 |
| 2 | 精校 |
| 3 | AI整理 |

`AI整理` 的书默认不导出（可用 `-status AI整理` 单独取）。

## 注意事项

- 搜索结果总数会被服务端钳到 1000（如「诗集 + 集部」显示 1000，实际更多）；本例 489 未触顶。
- 站点文字为繁体原文，脚本不做简繁转换，输出与卡片一致。
- 页面结构靠 `.semi-tree-wrapper` / `.semi-tabs-tab` / `li.semi-tree-option` 这些半设计体系语义类名，
  不含构建哈希；若站点改版，先跑 `-v -visible` 看卡在哪一步。
- 实测 50 页约 1.5–2 分钟（每页 1–2 秒，主要花在页面渲染），比逐页点击人工操作快得多。
