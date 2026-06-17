package subscription

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/tenebra-vpn/tenebra/core/model"
)

func parseHysteria2(raw string) (model.Node, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return model.Node{}, fmt.Errorf("subscription: hysteria2: %w", err)
	}
	host, port, err := hostPort(u.Host)
	if err != nil {
		return model.Node{}, fmt.Errorf("subscription: hysteria2: %w", err)
	}

	q := u.Query()
	n := model.Node{
		Protocol: model.Hysteria2,
		Name:     fragmentName(u),
		Server:   host,
		Port:     port,
		// The password sits in the userinfo; some generators url-encode it.
		Password: userinfoSecret(u),
	}

	n.TLS = &model.TLS{
		Enabled:    true,
		ServerName: q.Get("sni"),
		Insecure:   isTruthy(q.Get("insecure")),
		ALPN:       splitALPN(q.Get("alpn")),
	}

	if obfs := strings.ToLower(q.Get("obfs")); obfs == "salamander" {
		n.Obfs = &model.Obfs{Type: "salamander", Password: q.Get("obfs-password")}
	}

	return n, nil
}

// userinfoSecret returns the password carried in userinfo. Hysteria2 puts the
// whole secret in the username slot (no colon), but tolerate "user:pass" too.
func userinfoSecret(u *url.URL) string {
	if pw, ok := u.User.Password(); ok {
		return pw
	}
	return u.User.Username()
}

func isTruthy(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
