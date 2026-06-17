package subscription

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

// userAgent is a browser-like UA; some subscription panels gate responses on it.
const userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36"

// maxBody caps a subscription response at 8 MiB to bound memory use.
const maxBody = 8 << 20

// Fetch retrieves a subscription body over HTTP(S). It is intentionally the
// only network-touching function in the package; parsing is done separately so
// it can be tested offline.
func Fetch(ctx context.Context, url string) (body []byte, header http.Header, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("subscription: fetch: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "*/*")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("subscription: fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, resp.Header, fmt.Errorf("subscription: fetch: unexpected status %s", resp.Status)
	}

	body, err = io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, resp.Header, fmt.Errorf("subscription: fetch: read body: %w", err)
	}
	return body, resp.Header, nil
}
