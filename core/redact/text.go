// Package redact sanitizes text before it leaves private application state.
package redact

import (
	"net"
	"net/url"
	"regexp"
	"strings"

	"github.com/Divaaaan/tenebra/core/model"
	"github.com/Divaaaan/tenebra/core/nodeidentity"
)

var genericURL = regexp.MustCompile(`(?i)\b(?:https?|vless|vmess|trojan|ssr?|hysteria2?|hy2|tuic|socks5?|wireguard|amneziawg|awg)://[^\s<>"'\x60]+`)
var bareUUID = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)

func Text(text string) string {
	text = genericURL.ReplaceAllStringFunc(text, func(raw string) string {
		if marker := strings.Index(strings.ToLower(raw), "://[redacted]"); marker > 0 {
			tail := raw[marker+len("://[redacted]"):]
			if strings.Trim(tail, ".,;)]}") == "" {
				return strings.ToLower(raw[:marker]) + "://[redacted]" + tail
			}
		}
		trimmed := strings.TrimRight(raw, ".,;)]}")
		suffix := raw[len(trimmed):]
		value, err := url.Parse(trimmed)
		if err != nil {
			return "[redacted-url]" + suffix
		}
		scheme := strings.ToLower(value.Scheme)
		if scheme != "https" && scheme != "http" {
			// A second boundary sanitizes an already-safe endpoint again. An
			// opaque base64 share blob has no dotted/numeric hostname and never
			// gets promoted into one merely because url.Parse calls it a Host.
			safeEndpoint := value.Hostname() != "" && (value.User != nil || strings.Contains(value.Hostname(), ".") || net.ParseIP(value.Hostname()) != nil)
			if scheme != "vmess" && scheme != "ssr" && safeEndpoint {
				return scheme + "://" + value.Host + suffix
			}
			return scheme + "://[redacted]" + suffix
		}
		if value.Hostname() == "" {
			return "[redacted-url]" + suffix
		}
		return scheme + "://" + value.Host + suffix
	})
	text = bareUUID.ReplaceAllString(text, "***")
	return nodeidentity.RedactLegacyIDs(text)
}

func ProfileName(name, rawURL string) string {
	name = strings.TrimSpace(name)
	if name == "" || name == strings.TrimSpace(rawURL) || genericURL.MatchString(name) {
		if value, err := url.Parse(rawURL); err == nil && value.Hostname() != "" {
			return Text(value.Hostname())
		}
		if name != "" {
			if value, err := url.Parse(name); err == nil && value.Hostname() != "" {
				return Text(value.Hostname())
			}
		}
		return "Subscription"
	}
	return Text(name)
}

func NodeName(node model.Node) string {
	name := node.Name
	secrets := []string{node.Password, node.UUID}
	if node.Obfs != nil {
		secrets = append(secrets, node.Obfs.Password)
	}
	if node.WireGuard != nil {
		secrets = append(secrets, node.WireGuard.PrivateKey, node.WireGuard.PreSharedKey)
	}
	for _, secret := range secrets {
		if secret != "" {
			name = strings.ReplaceAll(name, secret, "***")
		}
	}
	name = strings.TrimSpace(Text(name))
	if name == "" {
		return "Proxy server"
	}
	return name
}
