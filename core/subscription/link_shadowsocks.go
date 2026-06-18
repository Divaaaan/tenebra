package subscription

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/Divaaaan/tenebra/core/model"
)

// parseShadowsocks handles both the SIP002 form
//
//	ss://base64(method:password)@host:port#name
//
// and the legacy fully-base64 form
//
//	ss://base64(method:password@host:port)#name
func parseShadowsocks(raw string) (model.Node, error) {
	body := strings.TrimPrefix(raw, "ss://")

	// Peel off the #fragment ourselves; in the legacy form the part before it
	// is not a parseable URL.
	var name string
	if i := strings.IndexByte(body, '#'); i >= 0 {
		if dec, err := url.PathUnescape(body[i+1:]); err == nil {
			name = dec
		} else {
			name = body[i+1:]
		}
		body = body[:i]
	}

	// Pull off an optional plugin query before deciding on the form.
	var plugin string
	if i := strings.IndexByte(body, '?'); i >= 0 {
		if q, err := url.ParseQuery(body[i+1:]); err == nil {
			plugin = q.Get("plugin")
		}
		body = body[:i]
	}

	var method, password, host string
	var port int

	if at := strings.LastIndexByte(body, '@'); at >= 0 {
		// SIP002: userinfo is base64(method:password), authority is plain.
		userinfo := body[:at]
		if dec, err := decodeBase64(userinfo); err == nil {
			userinfo = string(dec)
		}
		m, p, ok := strings.Cut(userinfo, ":")
		if !ok {
			return model.Node{}, fmt.Errorf("subscription: ss: malformed method:password")
		}
		method, password = m, p

		h, prt, err := hostPort(body[at+1:])
		if err != nil {
			return model.Node{}, fmt.Errorf("subscription: ss: %w", err)
		}
		host, port = h, prt
	} else {
		// Legacy: the whole thing is base64(method:password@host:port). Split on
		// the LAST '@' (like the SIP002 branch) so a password containing '@' isn't
		// truncated — only the host:port authority follows the final separator.
		dec, err := decodeBase64(body)
		if err != nil {
			return model.Node{}, fmt.Errorf("subscription: ss: base64: %w", err)
		}
		decStr := string(dec)
		at := strings.LastIndexByte(decStr, '@')
		if at < 0 {
			return model.Node{}, fmt.Errorf("subscription: ss: malformed legacy link")
		}
		creds, authority := decStr[:at], decStr[at+1:]
		m, p, ok := strings.Cut(creds, ":")
		if !ok {
			return model.Node{}, fmt.Errorf("subscription: ss: malformed method:password")
		}
		method, password = m, p

		h, prt, err := hostPort(authority)
		if err != nil {
			return model.Node{}, fmt.Errorf("subscription: ss: %w", err)
		}
		host, port = h, prt
	}

	if method == "" {
		return model.Node{}, fmt.Errorf("subscription: ss: empty method")
	}
	if password == "" {
		return model.Node{}, fmt.Errorf("subscription: ss: empty password")
	}

	n := model.Node{
		Protocol: model.Shadowsocks,
		Name:     name,
		Server:   host,
		Port:     port,
		Method:   method,
		Password: password,
	}
	if plugin != "" {
		n.Extra = map[string]string{"plugin": plugin}
	}
	return n, nil
}
