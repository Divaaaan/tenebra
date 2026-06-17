package subscription

import (
	"strconv"
	"strings"
	"time"

	"github.com/tenebra-vpn/tenebra/core/model"
)

// ParseSubscription parses a subscription body into nodes. The body is most
// often base64 of a newline-separated link list; if a whole-body base64 decode
// succeeds it is used, otherwise the body is treated as plaintext. Lines that
// fail to parse are counted in skipped rather than failing the whole call.
func ParseSubscription(body []byte) (nodes []model.Node, skipped int, err error) {
	text := strings.TrimSpace(string(body))
	if text == "" {
		return nil, 0, nil
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
