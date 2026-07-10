package subscription

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Divaaaan/tenebra/core/model"
)

// parseSubscriptionHook is a test-only seam: when non-nil it runs inside
// ParseSubscription's recover-guarded body so a test can induce a panic and
// prove the recover turns it into an error return. It is always nil in
// production and adds one predictable-branch of overhead.
var parseSubscriptionHook func()

// ParseSubscription parses a subscription body into nodes. A body may be a
// Clash/Mihomo YAML config (servers under a top-level "proxies:" key), base64 of
// a newline-separated link list, or a plaintext link list. Entries that fail to
// parse are counted in skipped rather than failing the whole call.
func ParseSubscription(body []byte) (nodes []model.Node, skipped int, err error) {
	// Belt-and-suspenders around the hand-written decoders below: the depth cap
	// in the YAML reader already bounds recursion, but a future parser bug must
	// never be able to panic the process — ParseSubscription runs inside the
	// privileged KeepAlive daemon's auto-refresh worker, so a crash there would
	// take the whole daemon down and crash-loop on every refresh of a poisoned
	// subscription. Convert any panic into an ordinary error return instead.
	defer func() {
		if r := recover(); r != nil {
			nodes, skipped, err = nil, 0, fmt.Errorf("parse subscription: %v", r)
		}
	}()
	if parseSubscriptionHook != nil {
		parseSubscriptionHook() // test seam; nil in production
	}

	text := strings.TrimSpace(string(body))
	if text == "" {
		return nil, 0, nil
	}

	// A Clash/Mihomo config carries its servers as YAML under a top-level
	// "proxies:" key rather than as share links. Detect that shape first: a
	// base64 link list is a single unbroken token and a plaintext list starts
	// each line with a scheme, so neither trips this and the paths below are
	// unchanged for them. A leading UTF-8 BOM (which TrimSpace does not remove)
	// is stripped for this path only, since some servers prepend one.
	if clash := stripBOM(text); looksLikeClashConfig(clash) {
		return parseClashSubscription(clash)
	}

	if dec, derr := decodeBase64(text); derr == nil && looksLikeLinks(dec) {
		text = string(dec)
	}

	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "//") || strings.HasPrefix(line, "#") {
			continue
		}
		node, perr := ParseLink(line)
		if perr != nil {
			skipped++
			continue
		}
		nodes = append(nodes, node)
	}
	return nodes, skipped, nil
}

// stripBOM removes a leading UTF-8 byte-order mark (EF BB BF). TrimSpace leaves
// it in place, so it is trimmed explicitly where a leading marker would matter.
func stripBOM(s string) string {
	if len(s) >= 3 && s[0] == 0xEF && s[1] == 0xBB && s[2] == 0xBF {
		return s[3:]
	}
	return s
}

// looksLikeClashConfig reports whether the body is a Clash/Mihomo YAML config —
// that is, it has a top-level (unindented) "proxies:" key. An indented "proxies"
// (e.g. nested under a proxy-provider) or a "#"-commented line does not count.
func looksLikeClashConfig(text string) bool {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, " \t\r")
		if line == "" || line[0] == ' ' || line[0] == '\t' {
			continue // blank or indented: not a top-level key
		}
		if !strings.HasPrefix(line, "proxies:") {
			continue
		}
		// "proxies:" must be the whole key: the next character (if any) is a
		// space or the start of a flow list, never more identifier characters.
		rest := line[len("proxies:"):]
		if rest == "" || rest[0] == ' ' || rest[0] == '\t' || rest[0] == '[' {
			return true
		}
	}
	return false
}

// parseClashSubscription decodes the "proxies" list of a Clash/Mihomo config and
// maps each entry to a Node. A proxy of an unsupported type, or one missing the
// fields its protocol needs, is counted in skipped just like an unparseable link;
// the call never fails as a whole.
func parseClashSubscription(text string) (nodes []model.Node, skipped int, err error) {
	proxies, _ := clashProxyMaps([]byte(text))
	for _, p := range proxies {
		node, ok := clashProxyToNode(p)
		if !ok {
			skipped++
			continue
		}
		nodes = append(nodes, node)
	}
	return nodes, skipped, nil
}

// looksLikeLinks reports whether decoded bytes plausibly hold share links,
// guarding against treating arbitrary plaintext as base64 by coincidence.
func looksLikeLinks(b []byte) bool {
	return strings.Contains(string(b), "://")
}

// UserInfo is the parsed Subscription-Userinfo header. Expire is zero when the
// header omits it.
type UserInfo struct {
	Upload   int64
	Download int64
	Total    int64
	Expire   time.Time
}

// ParseUserInfo parses a Subscription-Userinfo header of the form
//
//	upload=0; download=123; total=456; expire=1700000000
//
// Unknown or malformed fields are ignored so a partial header still yields what
// it can.
func ParseUserInfo(h string) (UserInfo, error) {
	var info UserInfo
	for _, part := range strings.Split(h, ";") {
		key, val, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		switch strings.ToLower(key) {
		case "upload":
			info.Upload, _ = strconv.ParseInt(val, 10, 64)
		case "download":
			info.Download, _ = strconv.ParseInt(val, 10, 64)
		case "total":
			info.Total, _ = strconv.ParseInt(val, 10, 64)
		case "expire":
			if sec, err := strconv.ParseInt(val, 10, 64); err == nil && sec > 0 {
				info.Expire = time.Unix(sec, 0)
			}
		}
	}
	return info, nil
}
