package subscription

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/Divaaaan/tenebra/core/model"
)

func parseVLESS(raw string) (model.Node, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return model.Node{}, fmt.Errorf("subscription: vless: %w", err)
	}
	uuid := u.User.Username()
	if uuid == "" {
		return model.Node{}, fmt.Errorf("subscription: vless: missing uuid")
	}
	host, port, err := hostPort(u.Host)
	if err != nil {
		return model.Node{}, fmt.Errorf("subscription: vless: %w", err)
	}

	q := u.Query()
	n := model.Node{
		Protocol: model.VLESS,
		Name:     fragmentName(u),
		Server:   host,
		Port:     port,
		UUID:     uuid,
		Flow:     q.Get("flow"),
	}

	if tr := buildTransport(q); tr != nil {
		n.Transport = tr
	}

	switch strings.ToLower(q.Get("security")) {
	case "tls":
		n.TLS = buildTLS(q, false)
	case "reality":
		n.TLS = buildTLS(q, true)
	}

	return n, nil
}

// buildTransport reads transport-related query params shared by vless/trojan.
// It returns nil for tcp/raw/empty so the Node omits the field.
func buildTransport(q url.Values) *model.Transport {
	switch strings.ToLower(q.Get("type")) {
	case "", "tcp", "raw", "none":
		return nil
	case "ws", "websocket":
		return &model.Transport{Type: "ws", Path: q.Get("path"), Host: q.Get("host")}
	case "grpc":
		return &model.Transport{Type: "grpc", ServiceName: q.Get("serviceName")}
	case "http", "h2":
		return &model.Transport{Type: "http", Path: q.Get("path"), Host: q.Get("host")}
	case "httpupgrade":
		return &model.Transport{Type: "httpupgrade", Path: q.Get("path"), Host: q.Get("host")}
	default:
		return &model.Transport{Type: strings.ToLower(q.Get("type")), Path: q.Get("path"), Host: q.Get("host"), ServiceName: q.Get("serviceName")}
	}
}

// buildTLS reads the TLS/REALITY query params shared by vless/trojan.
func buildTLS(q url.Values, reality bool) *model.TLS {
	t := &model.TLS{
		Enabled:     true,
		ServerName:  q.Get("sni"),
		Fingerprint: q.Get("fp"),
		ALPN:        splitALPN(q.Get("alpn")),
	}
	if t.ServerName == "" {
		t.ServerName = q.Get("peer")
	}
	if reality {
		t.Reality = &model.Reality{
			PublicKey: q.Get("pbk"),
			ShortID:   q.Get("sid"),
			SpiderX:   q.Get("spx"),
		}
	}
	return t
}
