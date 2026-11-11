package installtx

import (
	"bytes"
	"encoding/json"
	"io"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"
)

type jsonValue = json.RawMessage

func decodeObject(data []byte, result *map[string]jsonValue) error {
	if json.Unmarshal(data, result) != nil || *result == nil {
		return ErrProtocol
	}
	return nil
}
func strictDecode(data []byte, result any) error {
	if len(data) == 0 || len(data) > MaxMetadataBytes || !validMetadataEncoding(data) {
		return ErrProtocol
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	var visit func(int) error
	visit = func(depth int) error {
		if depth > 32 {
			return ErrProtocol
		}
		token, err := d.Token()
		if err != nil {
			return ErrProtocol
		}
		switch token {
		case json.Delim('{'):
			seen := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return ErrProtocol
				}
				name, ok := key.(string)
				if !ok || seen[name] {
					return ErrProtocol
				}
				seen[name] = true
				if err := visit(depth + 1); err != nil {
					return err
				}
			}
			end, err := d.Token()
			if err != nil || end != json.Delim('}') {
				return ErrProtocol
			}
		case json.Delim('['):
			for d.More() {
				if err := visit(depth + 1); err != nil {
					return err
				}
			}
			end, err := d.Token()
			if err != nil || end != json.Delim(']') {
				return ErrProtocol
			}
		}
		return nil
	}
	if visit(0) != nil {
		return ErrProtocol
	}
	if _, err := d.Token(); err != io.EOF {
		return ErrProtocol
	}
	kind := reflect.TypeOf(result)
	if kind == nil || kind.Kind() != reflect.Pointer || strictShape(data, kind.Elem(), 0) != nil {
		return ErrProtocol
	}
	d = json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(result) != nil || d.Decode(new(any)) != io.EOF {
		return ErrProtocol
	}
	return nil
}

// Metadata has its own strict wire format; it does not depend on model/control
// decoding or accept the replacement characters encoding/json inserts for
// unpaired UTF-16 escapes. Valid escaped surrogate pairs remain supported.
func validMetadataEncoding(data []byte) bool {
	if !utf8.Valid(data) || !json.Valid(data) {
		return false
	}
	quoted := false
	for i := 0; i < len(data); i++ {
		if data[i] == '"' {
			quoted = !quoted
			continue
		}
		if !quoted || data[i] != '\\' {
			continue
		}
		if data[i+1] != 'u' {
			i++
			continue
		}
		value, err := strconv.ParseUint(string(data[i+2:i+6]), 16, 16)
		if err != nil {
			return false
		}
		if value >= 0xd800 && value <= 0xdbff {
			if i+12 > len(data) || data[i+6] != '\\' || data[i+7] != 'u' {
				return false
			}
			low, err := strconv.ParseUint(string(data[i+8:i+12]), 16, 16)
			if err != nil || low < 0xdc00 || low > 0xdfff {
				return false
			}
			i += 11
		} else {
			if value >= 0xdc00 && value <= 0xdfff {
				return false
			}
			i += 5
		}
	}
	return true
}
func strictShape(data []byte, kind reflect.Type, depth int) error {
	if depth > 32 {
		return ErrProtocol
	}
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		if kind.Kind() == reflect.Pointer {
			return nil
		}
		return ErrProtocol
	}
	for kind.Kind() == reflect.Pointer {
		kind = kind.Elem()
	}
	switch kind.Kind() {
	case reflect.Struct:
		var fields map[string]jsonValue
		if decodeObject(data, &fields) != nil {
			return ErrProtocol
		}
		known := map[string]bool{}
		for i := 0; i < kind.NumField(); i++ {
			field := kind.Field(i)
			if !field.IsExported() {
				continue
			}
			tag := field.Tag.Get("json")
			name, options, _ := strings.Cut(tag, ",")
			if name == "" || name == "-" {
				return ErrProtocol
			}
			known[name] = true
			value, ok := fields[name]
			if !ok {
				if !strings.Contains(options, "omitempty") {
					return ErrProtocol
				}
				continue
			}
			if err := strictShape(value, field.Type, depth+1); err != nil {
				return err
			}
		}
		for name := range fields {
			if !known[name] {
				return ErrProtocol
			}
		}
	case reflect.Slice:
		var values []jsonValue
		if json.Unmarshal(data, &values) != nil || values == nil {
			return ErrProtocol
		}
		for _, value := range values {
			if err := strictShape(value, kind.Elem(), depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}
func encodeMetadata(value any) ([]byte, error) {
	var b bytes.Buffer
	encoder := json.NewEncoder(&b)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if encoder.Encode(value) != nil || b.Len() > MaxMetadataBytes {
		return nil, ErrProtocol
	}
	return b.Bytes(), nil
}
func DecodeJournal(data []byte) (Journal, error) {
	var j Journal
	if err := strictDecode(data, &j); err != nil {
		return j, err
	}
	return j, j.Validate()
}
func EncodeJournal(j Journal) ([]byte, error) {
	if err := j.Validate(); err != nil {
		return nil, err
	}
	return encodeMetadata(j)
}
