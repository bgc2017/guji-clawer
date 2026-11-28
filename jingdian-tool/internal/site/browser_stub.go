//go:build !browser

package site

import (
	"context"
	"errors"
)

// searchViaBrowser is only compiled in with `go build -tags browser`.
func (c *Client) searchViaBrowser(ctx context.Context, req SearchRequest) ([]byte, error) {
	return nil, errors.New("this binary was built without the browser fallback; rebuild with: go build -tags browser")
}
