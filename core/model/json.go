package model

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"unicode/utf8"
)

var errInvalidObject = errors.New("invalid persisted JSON object")

func decodeObject(data []byte, value any, known []string) (map[string]json.RawMessage, error) {
	if ValidateJSONEncoding(data) != nil || len(bytes.TrimSpace(data)) == 0 || bytes.TrimSpace(data)[0] != '{' {
		return nil, errInvalidObject
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil || json.Unmarshal(data, value) != nil {
		return nil, errInvalidObject
	}
	for _, key := range known {
		delete(fields, key)
	}
	if len(fields) == 0 {
		return nil, nil
	}
	return fields, nil
}
func encodeObject(value any, extra map[string]json.RawMessage, known []string) ([]byte, error) {
	if !validObjectStrings(reflect.ValueOf(value), 0) {
		return nil, errInvalidObject
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, errInvalidObject
	}
	if len(extra) == 0 {
		return data, nil
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil {
		return nil, errInvalidObject
	}
	reserved := make(map[string]bool, len(known))
	for _, key := range known {
		reserved[key] = true
	}
	for key, value := range extra {
		if reserved[key] {
			continue
		}
		if !utf8.ValidString(key) || ValidateJSONEncoding(value) != nil {
			return nil, errInvalidObject
		}
		fields[key] = value
	}
	return json.Marshal(fields)
}

var rawJSONType = reflect.TypeOf(json.RawMessage(nil))

func validObjectStrings(value reflect.Value, depth int) bool {
	if depth > 128 {
		return false
	}
	if !value.IsValid() {
		return true
	}
	if value.Type() == rawJSONType {
		return ValidateJSONEncoding(value.Bytes()) == nil
	}
	switch value.Kind() {
	case reflect.String:
		return utf8.ValidString(value.String())
	case reflect.Pointer, reflect.Interface:
		return value.IsNil() || validObjectStrings(value.Elem(), depth+1)
	case reflect.Struct:
		for i := 0; i < value.NumField(); i++ {
			if value.Type().Field(i).IsExported() && !validObjectStrings(value.Field(i), depth+1) {
				return false
			}
		}
	case reflect.Map:
		iterator := value.MapRange()
		for iterator.Next() {
			if !validObjectStrings(iterator.Key(), depth+1) || !validObjectStrings(iterator.Value(), depth+1) {
				return false
			}
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < value.Len(); i++ {
			if !validObjectStrings(value.Index(i), depth+1) {
				return false
			}
		}
	}
	return true
}

var nodeKeys = []string{"protocol", "name", "server", "port", "uuid", "password", "method", "flow", "security", "alter_id", "tls", "transport", "obfs", "wireguard", "extra"}

func (v Node) MarshalJSON() ([]byte, error) {
	type plain Node
	return encodeObject(plain(v), v.AdditionalFields, nodeKeys)
}
func (v *Node) UnmarshalJSON(data []byte) error {
	type plain Node
	var value plain
	fields, err := decodeObject(data, &value, nodeKeys)
	if err != nil {
		return err
	}
	*v = Node(value)
	v.AdditionalFields = fields
	return nil
}

var tlsKeys = []string{"enabled", "server_name", "insecure", "alpn", "fingerprint", "fragment", "reality"}

func (v TLS) MarshalJSON() ([]byte, error) {
	type plain TLS
	return encodeObject(plain(v), v.AdditionalFields, tlsKeys)
}
func (v *TLS) UnmarshalJSON(data []byte) error {
	type plain TLS
	var value plain
	fields, err := decodeObject(data, &value, tlsKeys)
	if err != nil {
		return err
	}
	*v = TLS(value)
	v.AdditionalFields = fields
	return nil
}

var realityKeys = []string{"public_key", "short_id", "spider_x"}

func (v Reality) MarshalJSON() ([]byte, error) {
	type plain Reality
	return encodeObject(plain(v), v.AdditionalFields, realityKeys)
}
func (v *Reality) UnmarshalJSON(data []byte) error {
	type plain Reality
	var value plain
	fields, err := decodeObject(data, &value, realityKeys)
	if err != nil {
		return err
	}
	*v = Reality(value)
	v.AdditionalFields = fields
	return nil
}

var transportKeys = []string{"type", "path", "host", "service_name"}

func (v Transport) MarshalJSON() ([]byte, error) {
	type plain Transport
	return encodeObject(plain(v), v.AdditionalFields, transportKeys)
}
func (v *Transport) UnmarshalJSON(data []byte) error {
	type plain Transport
	var value plain
	fields, err := decodeObject(data, &value, transportKeys)
	if err != nil {
		return err
	}
	*v = Transport(value)
	v.AdditionalFields = fields
	return nil
}

var obfsKeys = []string{"type", "password"}

func (v Obfs) MarshalJSON() ([]byte, error) {
	type plain Obfs
	return encodeObject(plain(v), v.AdditionalFields, obfsKeys)
}
func (v *Obfs) UnmarshalJSON(data []byte) error {
	type plain Obfs
	var value plain
	fields, err := decodeObject(data, &value, obfsKeys)
	if err != nil {
		return err
	}
	*v = Obfs(value)
	v.AdditionalFields = fields
	return nil
}

var wireguardKeys = []string{"private_key", "peer_public_key", "pre_shared_key", "local_address", "jc", "jmin", "jmax", "s1", "s2", "h1", "h2", "h3", "h4"}

func (v WireGuard) MarshalJSON() ([]byte, error) {
	type plain WireGuard
	return encodeObject(plain(v), v.AdditionalFields, wireguardKeys)
}
func (v *WireGuard) UnmarshalJSON(data []byte) error {
	type plain WireGuard
	var value plain
	fields, err := decodeObject(data, &value, wireguardKeys)
	if err != nil {
		return err
	}
	*v = WireGuard(value)
	v.AdditionalFields = fields
	return nil
}
