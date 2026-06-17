package subscription

import (
	"fmt"
	"net/url"

	"github.com/tenebra-vpn/tenebra/core/model"
)

func parseTrojan(raw string) (model.Node, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return model.Node{}, fmt.Errorf("subscription: trojan: %w", err)
	}
	password := userinfoSecret(u)
	if password == "" {
		return model.Node{}, fmt.Errorf("subscription: trojan: missing password")
	}
	host, port, err := hostPort(u.Host)
	if err != nil {
		return model.Node{}, fmt.Errorf("subscription: trojan: %w", err)
	}

	q := u.Query()
	n := model.Node{
		Protocol: model.Trojan,
		Name:     fragmentName(u),
		Server:   host,
		Port:     port,
		Password: password,
	}

	if tr := buildTransport(q); tr != nil {
		n.Transport = tr
	}

	// Trojan runs over TLS by default.
	n.TLS = &model.TLS{
		Enabled:    true,
		ServerName: q.Get("sni"),
		Insecure:   isTruthy(q.Get("allowInsecure")),
		ALPN:       splitALPN(q.Get("alpn")),
	}

	return n, nil
}
