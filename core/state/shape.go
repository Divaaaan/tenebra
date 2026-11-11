package state

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"time"
)

var shapeFields sync.Map
var rawMessageType = reflect.TypeOf(json.RawMessage(nil))
var timeType = reflect.TypeOf(time.Time{})

func structFields(kind reflect.Type) map[string]reflect.Type {
	if cached, ok := shapeFields.Load(kind); ok {
		return cached.(map[string]reflect.Type)
	}
	fields := map[string]reflect.Type{}
	for i := 0; i < kind.NumField(); i++ {
		field := kind.Field(i)
		if !field.IsExported() {
			continue
		}
		tag, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if tag == "-" {
			continue
		}
		if field.Anonymous && tag == "" {
			nested := field.Type
			for nested.Kind() == reflect.Pointer {
				nested = nested.Elem()
			}
			if nested.Kind() == reflect.Struct {
				for key, value := range structFields(nested) {
					fields[key] = value
				}
				continue
			}
		}
		if tag == "" {
			tag = field.Name
		}
		fields[tag] = field.Type
	}
	actual, _ := shapeFields.LoadOrStore(kind, fields)
	return actual.(map[string]reflect.Type)
}

// encoding/json treats null as the zero value of a non-pointer bool/string and
// matches struct keys without regard to case. Both can silently erase an intent.
// Unknown extension keys remain case-sensitive and retain their original JSON.
func validateShape(data []byte, kind reflect.Type, depth int) error {
	if depth > 256 {
		return ErrCorruptState
	}
	if kind == rawMessageType {
		return nil
	}
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		switch kind.Kind() {
		case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Interface:
			return nil
		default:
			return ErrCorruptState
		}
	}
	for kind.Kind() == reflect.Pointer {
		kind = kind.Elem()
	}
	if kind == timeType {
		return nil
	} // Its own decoder validates the RFC3339 string.
	switch kind.Kind() {
	case reflect.Struct:
		var fields map[string]json.RawMessage
		if json.Unmarshal(data, &fields) != nil || fields == nil {
			return ErrCorruptState
		}
		known := structFields(kind)
		for key, value := range fields {
			field, ok := known[key]
			if !ok {
				for name := range known {
					if strings.EqualFold(key, name) {
						return ErrCorruptState
					}
				}
				continue
			}
			if err := validateShape(value, field, depth+1); err != nil {
				return err
			}
		}
	case reflect.Map:
		var fields map[string]json.RawMessage
		if json.Unmarshal(data, &fields) != nil {
			return ErrCorruptState
		}
		for _, value := range fields {
			if err := validateShape(value, kind.Elem(), depth+1); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		var values []json.RawMessage
		if json.Unmarshal(data, &values) != nil {
			return ErrCorruptState
		}
		for _, value := range values {
			if err := validateShape(value, kind.Elem(), depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}
