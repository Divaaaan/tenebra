package model

import (
	"encoding/json"
	"testing"
)

func TestJSONCredentialEncodingNeverReplacesUnpairedSurrogates(t *testing.T) {
	for _, raw := range []string{`{"password":"\ud800"}`, `{"tls":{"server_name":"\udfff"}}`, `{"future":"\ud800\u0041"}`, `{"future":"\udc00\ud800"}`} {
		if ValidateJSONEncoding([]byte(raw)) == nil {
			t.Fatal("accepted lossy JSON string encoding")
		}
		var node Node
		if json.Unmarshal([]byte(raw), &node) == nil {
			t.Fatal("node decoder silently replaced malformed credential/unknown field")
		}
	}
	for _, raw := range []string{`{"name":"\ud83d\ude00"}`, `{"name":"ordinary \\ud800 text"}`, `{"name":"Москва"}`, `{"future":{"key":"\u0000"}}`} {
		if err := ValidateJSONEncoding([]byte(raw)); err != nil {
			t.Fatal("valid encoding refused", err)
		}
	}
}

func TestJSONCredentialEncodingRejectsInvalidGoUTF8BeforeMarshal(t *testing.T) {
	invalid := string([]byte{0xff})
	for _, node := range []Node{{Password: invalid}, {TLS: &TLS{ServerName: invalid}}, {Extra: map[string]string{"key": invalid}}, {AdditionalFields: map[string]json.RawMessage{"future": json.RawMessage(`"\ud800"`)}}} {
		if _, err := json.Marshal(node); err == nil {
			t.Fatal("invalid Go credential was silently encoded as a different string")
		}
	}
}
