// Package subscription parses proxy share links and subscription bodies into
// the normalized model.Node form consumed by the sing-box config generator.
//
// Parsing is deliberately kept free of any network access so it stays fully
// testable offline; fetching a remote subscription lives in Fetch.
package subscription

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/tenebra-vpn/tenebra/core/model"
)

// ErrUnsupportedScheme is returned for link schemes we knowingly do not parse,
// as opposed to input that is merely malformed.
var ErrUnsupportedScheme = errors.New("subscription: unsupported link scheme")

// ParseLink routes a single share link to the parser for its scheme.
func ParseLink(raw string) (model.Node, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return model.Node{}, errors.New("subscription: empty link")
	}
	scheme, _, ok := strings.Cut(raw, "://")
	if !ok {
		return model.Node{}, fmt.Errorf("subscription: missing scheme in %q", clip(raw))
	}
	switch strings.ToLower(scheme) {
	case "vless":
		return parseVLESS(raw)
	case "hysteria2", "hy2":
		return parseHysteria2(raw)
	case "ss":
		return parseShadowsocks(raw)
	case "trojan":
		return parseTrojan(raw)
	case "vmess":
		return parseVMess(raw)
	case "wireguard", "amneziawg", "awg":
		// AmneziaWG has no agreed single-line URL; configs arrive as full
		// wg-quick files imported by the profile package.
		return model.Node{}, fmt.Errorf("%w: %s (import the full AmneziaWG config instead)", ErrUnsupportedScheme, scheme)
	default:
		return model.Node{}, fmt.Errorf("%w: %s", ErrUnsupportedScheme, scheme)
	}
}

// hostPort splits a "host:port" authority, tolerating bracketed IPv6 literals,
// and validates the port. A missing or non-numeric port is an error.
func hostPort(authority string) (host string, port int, err error) {
	if authority == "" {
		return "", 0, errors.New("subscription: empty host:port")
	}
	h, p, err := net.SplitHostPort(authority)
	if err != nil {
		return "", 0, fmt.Errorf("subscription: invalid host:port %q: %w", authority, err)
	}
	host = strings.Trim(h, "[]")
	if host == "" {
		return "", 0, fmt.Errorf("subscription: empty host in %q", authority)
	}
	port, err = strconv.Atoi(p)
	if err != nil || port <= 0 || port > 65535 {
		return "", 0, fmt.Errorf("subscription: invalid port %q", p)
	}
	return host, port, nil
}

// fragmentName returns the URL-decoded #fragment, falling back to the raw value
// if it is not valid percent-encoding.
func fragmentName(u *url.URL) string {
	if u.Fragment == "" {
		return ""
	}
	if dec, err := url.PathUnescape(u.Fragment); err == nil {
		return dec
	}
	return u.Fragment
}

// splitALPN parses a comma-separated alpn list, trimming blanks.
func splitALPN(v string) []string {
	if v == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(v, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// decodeBase64 decodes std or url-safe base64, with or without padding.
func decodeBase64(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	// Normalize url-safe alphabet so a single decoder handles both.
	s = strings.ReplaceAll(s, "-", "+")
	s = strings.ReplaceAll(s, "_", "/")
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == ' ' || r == '\t' {
			return -1
		}
		return r
	}, s)
	if m := len(s) % 4; m != 0 {
		s += strings.Repeat("=", 4-m)
	}
	return base64.StdEncoding.DecodeString(s)
}

// clip shortens a string for error messages so we never echo a whole link.
func clip(s string) string {
	const max = 32
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
