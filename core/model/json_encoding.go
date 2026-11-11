package model

import (
	"encoding/json"
	"strconv"
	"unicode/utf8"
)

// ValidateJSONEncoding refuses encodings that encoding/json would replace by
// U+FFFD. A malformed credential must fail, not acquire a different identity.
func ValidateJSONEncoding(data []byte) error {
	if !utf8.Valid(data) || !json.Valid(data) {
		return errInvalidObject
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
		if i+1 >= len(data) {
			return errInvalidObject
		}
		if data[i+1] != 'u' {
			i++
			continue
		}
		if i+6 > len(data) {
			return errInvalidObject
		}
		value, err := strconv.ParseUint(string(data[i+2:i+6]), 16, 16)
		if err != nil {
			return errInvalidObject
		}
		if value >= 0xd800 && value <= 0xdbff {
			if i+12 > len(data) || data[i+6] != '\\' || data[i+7] != 'u' {
				return errInvalidObject
			}
			low, err := strconv.ParseUint(string(data[i+8:i+12]), 16, 16)
			if err != nil || low < 0xdc00 || low > 0xdfff {
				return errInvalidObject
			}
			i += 11
		} else {
			if value >= 0xdc00 && value <= 0xdfff {
				return errInvalidObject
			}
			i += 5
		}
	}
	return nil
}
