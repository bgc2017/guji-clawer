package main

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

func TestExtract(t *testing.T) {
	ctx, cancel := newBrowser(options{profile: t.TempDir(), timeout: 30 * time.Second})
	defer cancel()

	html := `<!doctype html><html><body>
<div class="result-page-wrapper">
 <div class="iwc-table-content">
  <div class="iwc-table-fixed">
   <div class="iwc-table-header"><ul>
     <li class="nohover"><div class="cell-box"></div></li>
     <li class="checkbox-box"><div class="cell-box"><i class="tr-checkbox"></i></div></li>
     <li><div class="cell-box"><span class="thead-span">股票代码</span></div></li>
     <li><div class="cell-box"><span class="thead-span">股票简称</span></div></li>
   </ul></div>
   <div class="iwc-table-body-outer"><table><tbody>
     <tr>
       <td><div class="td-cell-box no-padding">1</div></td>
       <td class="checkbox-box"></td>
       <td>000001</td><td>平安银行</td>
     </tr>
   </tbody></table></div>
  </div>
  <div class="iwc-table-scroll">
   <div class="iwc-table-header"><ul>
     <li><div class="cell-box"><span class="thead-span">最新价</span></div></li>
     <li><div class="cell-box"><span class="thead-span">涨跌幅</span></div></li>
   </ul></div>
   <div class="iwc-table-body"><table><tbody>
     <tr>
       <td><div class="td-cell-box no-padding">1</div></td>
       <td class="checkbox-box"></td>
       <td>11.20</td><td>1.23%</td>
     </tr>
   </tbody></table></div>
  </div>
 </div>
</div>
</body></html>`

	f, _ := os.CreateTemp(t.TempDir(), "*.html")
	f.WriteString(html)
	f.Close()
	if err := chromedp.Do(ctx, chromedp.Navigate("file://"+f.Name()), chromedp.Sleep(500*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	raw, err := chromedp.Run(ctx, chromedp.Evaluate[string](extractJS))
	if err != nil {
		t.Fatal(err)
	}
	var pd pageData
	if err := json.Unmarshal([]byte(raw), &pd); err != nil {
		t.Fatal(err, raw)
	}
	t.Logf("headers=%v rows=%v", pd.Headers, pd.Rows)
	if len(pd.Headers) != 4 {
		t.Fatalf("headers: %v", pd.Headers)
	}
	if len(pd.Rows) != 1 || len(pd.Rows[0]) != 4 {
		t.Fatalf("rows: %v", pd.Rows)
	}
	out, err := projectRows(pd.Rows, pd.Headers, []string{"股票简称", "最新价"})
	if err != nil {
		t.Fatal(err)
	}
	if out[0][0] != "平安银行" || out[0][1] != "11.20" {
		t.Fatalf("projected: %v", out)
	}
}
