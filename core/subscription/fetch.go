package subscription

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log"
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

	// Cloudflare and some subscription panels ask for a TLS renegotiation
	// mid-handshake. Go's default is RenegotiateNever, which fails the request
	// with "tls: no renegotiation" — so a URL that loads fine in curl or a
	// browser fails here. Clone the default transport (keeping its proxy, HTTP/2
	// and dial defaults) and permit a single client-initiated renegotiation.
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{}
	}
	transport.TLSClientConfig.Renegotiation = tls.RenegotiateOnceAsClient

	client := &http.Client{Timeout: 30 * time.Second, Transport: transport}
	resp, err := client.Do(req)
	if err != nil {
		// Log the real cause (host only, never the token-bearing path) so a
		// failed import leaves a trail in core.log instead of a silent generic.
		log.Printf("tenebra-core: subscription fetch failed for %s: %v", req.URL.Host, err)
		return nil, nil, fmt.Errorf("subscription: fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Printf("tenebra-core: subscription fetch got %s from %s", resp.Status, req.URL.Host)
		return nil, resp.Header, fmt.Errorf("subscription: fetch: unexpected status %s", resp.Status)
	}

	body, err = io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		log.Printf("tenebra-core: subscription read body failed for %s: %v", req.URL.Host, err)
		return nil, resp.Header, fmt.Errorf("subscription: fetch: read body: %w", err)
	}
	return body, resp.Header, nil
}
