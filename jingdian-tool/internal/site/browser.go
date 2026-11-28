//go:build browser

package site

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/chromedp/chromedp"
)

// searchViaBrowser performs the same POST from inside a real Chrome tab. This
// keeps working when the plain HTTP client trips the gateway's bot checks
// (captcha / IP ban), and it also carries the SESSION cookie the site sets on
// the search page.
func (c *Client) searchViaBrowser(ctx context.Context, req SearchRequest) ([]byte, error) {
	opts := chromedp.DefaultExecAllocatorOptions[:]
	opts = append(opts,
		chromedp.Flag("headless", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("no-first-run", true),
		chromedp.Flag("no-default-browser-check", true),
	)
	actx, cancel := chromedp.NewExecAllocator(ctx, opts...)
	defer cancel()
	tctx, cancel := chromedp.NewContext(actx)
	defer cancel()
	tctx, cancel = context.WithTimeout(tctx, 60*time.Second)
	defer cancel()

	if err := chromedp.Do(tctx,
		chromedp.Navigate(baseURL+"/6/search"),
		chromedp.Sleep(1500*time.Millisecond),
	); err != nil {
		return nil, fmt.Errorf("browser navigate: %w", err)
	}

	payload, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	script := fmt.Sprintf(`(async () => {
  const r = await fetch('/v1/search', {
    method: 'POST',
    credentials: 'include',
    headers: {'Content-Type': 'application/json', 'X-Requested-With': 'XMLHttpRequest', 'siteid': '1'},
    body: %q
  });
  return await r.text();
})()`, string(payload))

	body, err := chromedp.Run(tctx, chromedp.Evaluate[string](script, chromedp.EvalAwaitPromise))
	if err != nil {
		return nil, fmt.Errorf("browser search: %w", err)
	}
	return []byte(body), nil
}
