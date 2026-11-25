package main

// 搜索书籍筛选导出工具：独立脚本，不依赖主下载器的其他源码。
//
// 用法示例：
//   sdguji-search -q 诗集 -dynasty 清 -cate 集部-别集类 -out 诗集_清_别集类.txt
//
// 思路：搜索接口带字节签名（a_bogus），裸 HTTP 回放会被拒（errorCode 40001），
// 所以这里完整模拟浏览器：打开搜索页 → 点“搜书籍” → 在筛选面板勾选四部分类与朝代
// → 翻页；页面自己发出的每个 /search/book/v1 请求，用 CDP 把请求体和响应体一起抓下来解析。

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

const searchAPI = "/api/ancientlib/read/search/book/"

type searchReq struct {
	QueryText           string   `json:"queryText"`
	TraditionalCategory []string `json:"traditionalCategory"`
	DynastyCategory     []string `json:"dynastyCategory"`
	PageNum             int      `json:"pageNum"`
	PageSize            int      `json:"pageSize"`
	SearchID            string   `json:"searchId"`
}

type apiResp struct {
	Resp *SearchResp
	Req  searchReq
}

func (a *apiResp) tradSig() string { return strings.Join(a.Req.TraditionalCategory, ",") }
func (a *apiResp) dynSig() string  { return strings.Join(a.Req.DynastyCategory, ",") }

type collector struct {
	mu      sync.Mutex
	pending []pendingReq
	resps   []*apiResp
}

type pendingReq struct {
	id   network.RequestID
	req  searchReq
	page int
}

func (c *collector) listen(ctx context.Context) {
	chromedp.Listen(ctx, func(ev any) {
		e, ok := ev.(*network.EventRequestWillBeSent)
		if !ok {
			return
		}
		if e.Request.Method != "POST" || !strings.Contains(e.Request.URL, searchAPI) {
			return
		}
		var raw string
		for _, d := range e.Request.PostDataEntries {
			raw += string(d.Bytes)
		}
		var q searchReq
		_ = json.Unmarshal([]byte(raw), &q)
		c.mu.Lock()
		c.pending = append(c.pending, pendingReq{id: e.RequestID, req: q})
		c.mu.Unlock()
	})
}

func (c *collector) collect(ctx context.Context, verbose bool) {
	c.mu.Lock()
	ids := c.pending
	c.pending = nil
	c.mu.Unlock()

	for _, p := range ids {
		var body []byte
		for attempt := 0; attempt < 15 && len(body) == 0; attempt++ {
			_ = chromedp.Do(ctx, func(x context.Context, t *chromedp.Target) (chromedp.Void, error) {
				res, err := cdp.Call(x, t, network.GetResponseBody, network.GetResponseBodyParams{RequestID: p.id})
				if err == nil {
					body = res.Body
				}
				return chromedp.Void{}, err
			})
			if len(body) == 0 {
				time.Sleep(200 * time.Millisecond)
			}
		}
		if len(body) == 0 {
			if verbose {
				fmt.Fprintf(os.Stderr, "取响应体失败: %s\n", p.id)
			}
			continue
		}
		var r SearchResp
		if err := json.Unmarshal(body, &r); err != nil {
			if verbose {
				fmt.Fprintf(os.Stderr, "解析搜索响应失败: %v\n", err)
			}
			continue
		}
		if r.ErrorCode != 0 {
			if verbose {
				fmt.Fprintf(os.Stderr, "搜索接口返回错误 code=%d %s\n", r.ErrorCode, r.ErrorMsg)
			}
			continue
		}
		a := &apiResp{Resp: &r, Req: p.req}
		c.mu.Lock()
		dup := false
		for _, old := range c.resps {
			if old.Req.PageNum == a.Req.PageNum && old.tradSig() == a.tradSig() && old.dynSig() == a.dynSig() &&
				len(old.Resp.Data.BookList) == len(a.Resp.Data.BookList) {
				if len(a.Resp.Data.BookList) == 0 ||
					old.Resp.Data.BookList[0].Book.BookID == a.Resp.Data.BookList[0].Book.BookID {
					dup = true
					break
				}
			}
		}
		if !dup {
			c.resps = append(c.resps, a)
		}
		c.mu.Unlock()
	}
}

func (c *collector) snapshot() []*apiResp {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]*apiResp, len(c.resps))
	copy(out, c.resps)
	return out
}

func (c *collector) wait(ctx context.Context, from int, verbose bool, timeout time.Duration, pred func(*apiResp) bool) (*apiResp, int) {
	deadline := time.Now().Add(timeout)
	for {
		c.collect(ctx, verbose)
		all := c.snapshot()
		for i := from; i < len(all); i++ {
			if pred(all[i]) {
				return all[i], len(all)
			}
		}
		if time.Now().After(deadline) {
			return nil, len(all)
		}
		time.Sleep(300 * time.Millisecond)
	}
}

func evalMap(ctx context.Context, js string) map[string]any {
	r, err := chromedp.Run(ctx, chromedp.Evaluate[map[string]any](js))
	if err != nil {
		return map[string]any{"ok": false, "err": err.Error()}
	}
	return r
}

func q(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

const normJS = `const norm=(s)=>(s||'').replace(/\s+/g,'').replace(/[（(][^）)]*[）)]$/,'');`

func treeIndexes(ctx context.Context) (int, int) {
	m := evalMap(ctx, `(() => {`+normJS+`
	  const trees=[...document.querySelectorAll('.semi-tree-wrapper')];
	  let cate=-1, dyn=-1;
	  trees.forEach((t,i)=>{
	    const texts=[...t.querySelectorAll('li.semi-tree-option')].map(e=>norm(e.innerText));
	    if(texts.includes('经部')||texts.includes('集部')) cate=i;
	    if(texts.includes('唐')&&texts.includes('清')) dyn=i;
	  });
	  return {ok:true, cate:cate, dyn:dyn, n:trees.length};
	})()`)
	cate, dyn := -1, -1
	if v, ok := m["cate"].(float64); ok {
		cate = int(v)
	}
	if v, ok := m["dyn"].(float64); ok {
		dyn = int(v)
	}
	return cate, dyn
}

func clickTab(ctx context.Context) error {
	for i := 0; i < 10; i++ {
		m := evalMap(ctx, `(() => {
		  const tabs=[...document.querySelectorAll('.semi-tabs-tab')];
		  const t=tabs.find(e=>(e.innerText||'').replace(/\s+/g,'')==='搜书籍');
		  if(!t) return {ok:false,err:'no tab'};
		  if(t.getAttribute('aria-selected')==='true') return {ok:true,already:true};
		  t.click();
		  return {ok:true};
		})()`)
		if ok, _ := m["ok"].(bool); ok {
			return nil
		}
		time.Sleep(time.Second)
	}
	return fmt.Errorf("找不到“搜书籍”标签")
}

func treeAction(ctx context.Context, treeIdx int, label, action string) map[string]any {
	js := fmt.Sprintf(`(() => {`+normJS+`
	  const trees=[...document.querySelectorAll('.semi-tree-wrapper')];
	  const tree=trees[%d];
	  if(!tree) return {ok:false,err:'no tree %d'};
	  const items=[...tree.querySelectorAll('li.semi-tree-option')];
	  const li=items.find(e=>norm(e.innerText)===%s);
	  if(!li) return {ok:false,err:'not found',have:items.map(e=>norm(e.innerText)).slice(0,40)};
	  if(%s==='expand'){
	    if(!li.classList.contains('semi-tree-option-collapsed')) return {ok:true,already:true};
	    const ic=li.querySelector('.semi-tree-option-expand-icon');
	    if(ic) ic.click();
	    return {ok:true};
	  }
	  const cb=li.querySelector('[role=checkbox]');
	  const checked=cb && cb.getAttribute('aria-checked')==='true';
	  if(%s==='uncheck'){ if(checked) cb.click(); return {ok:true,was:checked}; }
	  if(checked) return {ok:true,already:true};
	  (cb||li).click();
	  return {ok:true};
	})()`, treeIdx, treeIdx, q(label), q(action), q(action))
	return evalMap(ctx, js)
}

func clickNextPage(ctx context.Context) map[string]any {
	return evalMap(ctx, `(() => {
	  const lis=[...document.querySelectorAll('li')].filter(e=>(e.innerText||'').trim()==='下一页');
	  if(!lis.length) return {ok:false,err:'no next li'};
	  const vis=lis.find(e=>e.offsetParent!==null)||lis[lis.length-1];
	  const cls=vis.className||'';
	  if(cls.includes('disabled')||vis.getAttribute('aria-disabled')==='true') return {ok:false,err:'disabled'};
	  vis.click();
	  return {ok:true};
	})()`)
}

func oneLine(s string) string {
	return strings.NewReplacer("\t", " ", "\r", " ", "\n", " ").Replace(s)
}

func main() {
	var (
		keyword  = flag.String("q", "", "搜索关键词（必填）")
		dynasty  = flag.String("dynasty", "", "朝代筛选，如 清；多个用逗号分隔")
		cate     = flag.String("cate", "", "四部分类筛选，如 集部-别集类；多个用逗号分隔")
		statuses = flag.String("status", "精校,粗校", "保留的校对状态，逗号分隔")
		exclude  = flag.String("exclude", "四库,四庫,四部", "来源命中这些词就排除，逗号分隔；传空字符串关闭")
		out      = flag.String("out", "", "输出文件；不填则打印到 stdout")
		head     = flag.Bool("header", false, "输出一行表头")
		lang     = flag.String("lang", "zh", "站点语言路径 zh / en")
		visible  = flag.Bool("visible", false, "显示浏览器窗口")
		chrome   = flag.String("chrome", "", "Chrome/Edge 可执行文件路径")
		ua       = flag.String("ua", "", "自定义 User-Agent")
		proxy    = flag.String("proxy", "", "代理，如 http://127.0.0.1:7890")
		maxPages = flag.Int("max-pages", 300, "最多翻多少页（保护）")
		timeout  = flag.Duration("timeout", 20*time.Minute, "整体超时")
		delay    = flag.Duration("delay", 600*time.Millisecond, "翻页间隔")
		verbose  = flag.Bool("v", false, "输出调试信息")
		dump     = flag.String("dump", "", "把抓到的原始书籍 JSON 存一份，便于排查")
	)
	flag.Parse()

	if *keyword == "" && flag.NArg() > 0 {
		*keyword = flag.Arg(0)
	}
	if *keyword == "" {
		fmt.Fprintln(os.Stderr, "缺少参数：-q 搜索关键词（例如 -q 诗集）")
		flag.Usage()
		os.Exit(2)
	}

	logf := func(format string, a ...any) {
		fmt.Fprintf(os.Stderr, format+"\n", a...)
	}

	ctx, cleanup, err := launchChrome(launchOptions{
		visible: *visible, chrome: *chrome, ua: *ua, proxy: *proxy, verbose: *verbose,
	})
	if err != nil {
		logf("启动浏览器失败: %v", err)
		os.Exit(1)
	}
	defer cleanup()

	tctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()

	c := &collector{}
	c.listen(tctx)

	target := fmt.Sprintf("https://www.shidianguji.com/%s/search/%s", *lang, url.PathEscape(*keyword))
	logf("打开: %s", target)
	if err := chromedp.Do(tctx, chromedp.Navigate(target)); err != nil {
		logf("打开搜索页失败: %v", err)
		os.Exit(1)
	}
	time.Sleep(4 * time.Second)
	if err := clickTab(tctx); err != nil {
		logf("%v", err)
		os.Exit(1)
	}

	base, idx := c.wait(tctx, 0, *verbose, 25*time.Second, func(r *apiResp) bool { return true })
	if base == nil {
		logf("没有抓到书籍搜索请求，页面可能未加载完成")
		os.Exit(1)
	}
	logf("初始结果: 共 %d 条（每页 %d）", base.Resp.Data.Total, base.Resp.Data.PageSize)

	cateIdx, dynIdx := treeIndexes(tctx)

	if *cate != "" {
		if cateIdx < 0 {
			logf("找不到四部分类筛选树")
			os.Exit(1)
		}
		for _, path := range strings.Split(*cate, ",") {
			path = strings.TrimSpace(path)
			if path == "" {
				continue
			}
			parts := strings.FieldsFunc(path, func(r rune) bool { return r == '-' || r == '/' || r == '>' })
			for _, p := range parts[:len(parts)-1] {
				m := treeAction(tctx, cateIdx, p, "expand")
				if ok, _ := m["ok"].(bool); !ok {
					logf("展开分类 %q 失败: %v", p, m["err"])
					os.Exit(1)
				}
				time.Sleep(700 * time.Millisecond)
			}
			leaf := parts[len(parts)-1]
			prevSig := ""
			if n := len(c.snapshot()); n > 0 {
				prevSig = c.snapshot()[n-1].tradSig()
			}
			m := treeAction(tctx, cateIdx, leaf, "check")
			if ok, _ := m["ok"].(bool); !ok {
				logf("勾选分类 %q 失败: %v（可选：%v）", leaf, m["err"], m["have"])
				os.Exit(1)
			}
			r, next := c.wait(tctx, idx, *verbose, 20*time.Second, func(r *apiResp) bool {
				return len(r.Req.TraditionalCategory) > 0 && r.tradSig() != prevSig
			})
			if r == nil {
				logf("等待分类筛选结果超时")
				os.Exit(1)
			}
			idx = next
			logf("分类 %s 已生效: 共 %d 条", path, r.Resp.Data.Total)
		}
	}

	if *dynasty != "" {
		if dynIdx < 0 {
			logf("找不到朝代筛选树")
			os.Exit(1)
		}
		for _, d := range strings.Split(*dynasty, ",") {
			d = strings.TrimSpace(d)
			if d == "" {
				continue
			}
			prevSig := ""
			if n := len(c.snapshot()); n > 0 {
				prevSig = c.snapshot()[n-1].dynSig()
			}
			m := treeAction(tctx, dynIdx, d, "check")
			if ok, _ := m["ok"].(bool); !ok {
				logf("勾选朝代 %q 失败: %v（可选：%v）", d, m["err"], m["have"])
				os.Exit(1)
			}
			r, next := c.wait(tctx, idx, *verbose, 20*time.Second, func(r *apiResp) bool {
				return len(r.Req.DynastyCategory) > 0 && r.dynSig() != prevSig
			})
			if r == nil {
				logf("等待朝代筛选结果超时")
				os.Exit(1)
			}
			idx = next
			logf("朝代 %s 已生效: 共 %d 条", d, r.Resp.Data.Total)
		}
	}

	all := c.snapshot()
	cur := all[len(all)-1]
	total := cur.Resp.Data.Total
	logf("筛选后共 %d 条", total)
	idx = len(all)

	// 翻页收集
	books := make([]Book, 0, total)
	seen := make(map[string]bool)
	addBooks := func(b *apiResp) int {
		n := 0
		for _, hit := range b.Resp.Data.BookList {
			if hit.Book.BookID == "" || seen[hit.Book.BookID] {
				continue
			}
			seen[hit.Book.BookID] = true
			books = append(books, hit.Book)
			n++
		}
		return n
	}
	addBooks(cur)
	logf("第 %d 页: 累计 %d 条", cur.Resp.Data.PageNum, len(books))

	for page := cur.Resp.Data.PageNum; page < *maxPages; {
		if !cur.Resp.Data.HasMore || (total > 0 && len(books) >= total) {
			break
		}
		m := clickNextPage(tctx)
		if ok, _ := m["ok"].(bool); !ok {
			logf("翻页结束: %v", m["err"])
			break
		}
		want := cur.Resp.Data.PageNum + 1
		r, next := c.wait(tctx, idx, *verbose, 30*time.Second, func(r *apiResp) bool {
			return r.Req.PageNum == want && r.tradSig() == cur.tradSig() && r.dynSig() == cur.dynSig()
		})
		if r == nil {
			logf("等待第 %d 页超时，已收 %d 条", want, len(books))
			break
		}
		idx = next
		cur = r
		page = r.Resp.Data.PageNum
		addBooks(cur)
		logf("第 %d 页: %d 条（累计 %d）", page, len(r.Resp.Data.BookList), len(books))
		time.Sleep(*delay)
	}

	logf("共收集 %d 条书籍记录", len(books))

	if *dump != "" {
		b, _ := json.MarshalIndent(books, "", " ")
		if err := os.WriteFile(*dump, b, 0o644); err != nil {
			logf("写 dump 失败: %v", err)
		}
	}

	keepStatus := map[string]bool{}
	for _, s := range strings.Split(*statuses, ",") {
		if s = strings.TrimSpace(s); s != "" {
			keepStatus[s] = true
		}
	}
	var excludes []string
	for _, s := range strings.Split(*exclude, ",") {
		if s = strings.TrimSpace(s); s != "" {
			excludes = append(excludes, s)
		}
	}

	type row struct{ id, name, dynasty, author string }
	rows := make([]row, 0, len(books))
	var dropSource, dropStatus int
	statusHist := map[string]int{}
	for _, b := range books {
		st := checkTypeName(b.CheckType)
		statusHist[st]++
		src := b.Edition.Edition + " " + b.PublicationStmt.Distributor
		excluded := false
		for _, kw := range excludes {
			if strings.Contains(src, kw) {
				excluded = true
				break
			}
		}
		if excluded {
			dropSource++
			continue
		}
		if !keepStatus[st] {
			dropStatus++
			continue
		}
		names := make([]string, 0, len(b.Authors))
		for _, a := range b.Authors {
			if a.PersName != "" {
				names = append(names, a.PersName)
			}
		}
		dyn := b.DynastyCategoryName
		if dyn == "" && len(b.Authors) > 0 {
			dyn = b.Authors[0].DynastyName
		}
		rows = append(rows, row{
			oneLine(b.BookID), oneLine(b.BookName), oneLine(dyn), oneLine(strings.Join(names, "、")),
		})
	}

	if *verbose {
		keys := make([]string, 0, len(statusHist))
		for k := range statusHist {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			logf("校对状态 %s: %d 条", k, statusHist[k])
		}
	}
	logf("来源排除 %d 条，状态过滤 %d 条，最终 %d 条", dropSource, dropStatus, len(rows))

	var sb strings.Builder
	if *head {
		sb.WriteString("ID\t书名\t朝代\t作者" + nl())
	}
	for _, r := range rows {
		sb.WriteString(r.id + "\t" + r.name + "\t" + r.dynasty + "\t" + r.author + nl())
	}
	if *out != "" {
		if err := os.WriteFile(*out, []byte(sb.String()), 0o644); err != nil {
			logf("写文件失败: %v", err)
			os.Exit(1)
		}
		logf("已写入 %s", *out)
	} else {
		fmt.Print(sb.String())
	}
}
