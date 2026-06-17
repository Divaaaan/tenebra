package subscription

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/tenebra-vpn/tenebra/core/model"
)

// vmessJSON mirrors the de-facto v2rayN vmess link payload. Numeric fields are
// frequently encoded as strings, so they are decoded leniently.
type vmessJSON struct {
	// v (version) is informational and clients encode it as either a number or
	// a string; keep it raw so neither form breaks decoding.
	V    json.RawMessage `json:"v"`
	PS   string          `json:"ps"`
	Add  string          `json:"add"`
	Port any             `json:"port"`
	ID   string          `json:"id"`
	Aid  any             `json:"aid"`
	Scy  string          `json:"scy"`
	Net  string          `json:"net"`
	Type string          `json:"type"`
	Host string          `json:"host"`
	Path string          `json:"path"`
	TLS  string          `json:"tls"`
	SNI  string          `json:"sni"`
	ALPN string          `json:"alpn"`
}

func parseVMess(raw string) (model.Node, error) {
	payload := strings.TrimPrefix(raw, "vmess://")
	dec, err := decodeBase64(payload)
	if err != nil {
		return model.Node{}, fmt.Errorf("subscription: vmess: base64: %w", err)
	}

	var v vmessJSON
	if err := json.Unmarshal(dec, &v); err != nil {
		return model.Node{}, fmt.Errorf("subscription: vmess: json: %w", err)
	}
	if v.Add == "" {
		return model.Node{}, fmt.Errorf("subscription: vmess: missing add")
	}
	port := toInt(v.Port)
	if port <= 0 || port > 65535 {
		return model.Node{}, fmt.Errorf("subscription: vmess: invalid port %v", v.Port)
	}
	if v.ID == "" {
		return model.Node{}, fmt.Errorf("subscription: vmess: missing id")
	}

	n := model.Node{
		Protocol: model.VMess,
		Name:     v.PS,
		Server:   v.Add,
		Port:     port,
		UUID:     v.ID,
		AlterID:  toInt(v.Aid),
		Security: v.Scy,
	}

	switch strings.ToLower(v.Net) {
	case "ws", "websocket":
		n.Transport = &model.Transport{Type: "ws", Path: v.Path, Host: v.Host}
	case "grpc":
		n.Transport = &model.Transport{Type: "grpc", ServiceName: v.Path}
	case "http", "h2":
		n.Transport = &model.Transport{Type: "http", Path: v.Path, Host: v.Host}
	case "httpupgrade":
		n.Transport = &model.Transport{Type: "httpupgrade", Path: v.Path, Host: v.Host}
	}

	if isTruthy(v.TLS) || strings.EqualFold(v.TLS, "tls") {
		n.TLS = &model.TLS{
			Enabled:    true,
			ServerName: v.SNI,
			ALPN:       splitALPN(v.ALPN),
		}
		if n.TLS.ServerName == "" {
			n.TLS.ServerName = v.Host
		}
	}

	return n, nil
}

// toInt accepts a JSON number or a numeric string and returns its int value,
// or 0 if it is neither.
func toInt(v any) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case json.Number:
		i, _ := x.Int64()
		return int(i)
	case string:
		i, _ := strconv.Atoi(strings.TrimSpace(x))
		return i
	default:
		return 0
	}
}
