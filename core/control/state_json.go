package control

import (
	"encoding/json"
	"reflect"
	"strings"
)

// MarshalJSON only encodes an already captured view. Revisioned snapshots carry
// every public field, including empty/off values, so replacing an older view
// cannot retain a retired connection or preference through omitempty.
func (s State) MarshalJSON() ([]byte, error) {
	type legacyState State
	if s.DaemonInstanceID == "" {
		return json.Marshal(legacyState(s))
	}
	value := reflect.ValueOf(legacyState(s))
	typ := value.Type()
	fields := make(map[string]json.RawMessage, typ.NumField())
	for index := 0; index < typ.NumField(); index++ {
		field := typ.Field(index)
		if !field.IsExported() {
			continue
		}
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		if name == "-" {
			continue
		}
		if name == "" {
			name = field.Name
		}
		part := value.Field(index)
		encoded, err := json.Marshal(part.Interface())
		if err != nil {
			return nil, err
		}
		fields[name] = encoded
	}
	return json.Marshal(fields)
}

func (event stateEvent) MarshalJSON() ([]byte, error) {
	if event.snapshot != nil {
		return json.Marshal(*event.snapshot)
	}
	type legacyEvent stateEvent
	return json.Marshal(legacyEvent(event))
}
