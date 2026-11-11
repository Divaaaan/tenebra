package state

import (
	"bytes"
	"encoding/json"
	"io"
	"reflect"
	"unicode/utf8"

	"github.com/Divaaaan/tenebra/core/model"
)

// Reject duplicate object keys before typed decoding. A duplicated selection,
// version or credential field must not acquire a first/last-wins interpretation.
func checkJSON(data []byte) error {
	if model.ValidateJSONEncoding(data) != nil {
		return ErrCorruptState
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var visit func(int) error
	visit = func(depth int) error {
		if depth > 256 {
			return ErrCorruptState
		}
		token, err := decoder.Token()
		if err != nil {
			return ErrCorruptState
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delimiter {
		case '{':
			keys := map[string]bool{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return ErrCorruptState
				}
				key, ok := keyToken.(string)
				if !ok || keys[key] {
					return ErrCorruptState
				}
				keys[key] = true
				if err := visit(depth + 1); err != nil {
					return err
				}
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim('}') {
				return ErrCorruptState
			}
		case '[':
			for decoder.More() {
				if err := visit(depth + 1); err != nil {
					return err
				}
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim(']') {
				return ErrCorruptState
			}
		default:
			return ErrCorruptState
		}
		return nil
	}
	if err := visit(0); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return ErrCorruptState
	}
	return nil
}

func objectFields(data []byte) (map[string]json.RawMessage, error) {
	if err := checkJSON(data); err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil || fields == nil {
		return nil, ErrCorruptState
	}
	return fields, nil
}

func mergeFields(value any, extra map[string]json.RawMessage, known []string) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, ErrCorruptState
	}
	if len(extra) == 0 {
		return data, nil
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil {
		return nil, ErrCorruptState
	}
	reserved := make(map[string]bool, len(known))
	for _, key := range known {
		reserved[key] = true
	}
	for key, value := range extra {
		if reserved[key] || !utf8.ValidString(key) || checkJSON(value) != nil {
			return nil, ErrCorruptState
		}
		fields[key] = value
	}
	data, err = json.Marshal(fields)
	if err != nil {
		return nil, ErrCorruptState
	}
	return data, nil
}

var snapshotKeys = []string{"schemaVersion", "identityVersion", "matcherVersion", "migrationId", "revision", "profiles", "profileOrder", "preferences", "connection", "multihop", "lastGood", "issues"}

func (s Snapshot) MarshalJSON() ([]byte, error) {
	type plain Snapshot
	return mergeFields(plain(s), s.AdditionalFields, snapshotKeys)
}
func (s *Snapshot) UnmarshalJSON(data []byte) error {
	fields, err := objectFields(data)
	if err != nil {
		return err
	}
	type plain Snapshot
	for _, key := range snapshotKeys {
		if _, present := fields[key]; !present {
			return ErrCorruptState
		}
	}
	if err := validateShape(data, reflect.TypeOf(plain{}), 0); err != nil {
		return err
	}
	var value plain
	if json.Unmarshal(data, &value) != nil {
		return ErrCorruptState
	}
	for _, key := range snapshotKeys {
		delete(fields, key)
	}
	if len(fields) == 0 {
		fields = nil
	}
	*s = Snapshot(value)
	s.AdditionalFields = fields
	return nil
}

var preferenceKeys = []string{"version", "split_mode", "split_apps", "routing_mode", "preset_games_direct", "preset_voice_direct", "preset_unblock_services", "kill_switch", "tls_fragment", "tun_stack", "proxy_mode", "proxy_port", "auto_failover", "ad_block", "dns_remote", "dns_direct", "ipv4_only", "rules_direct", "rules_proxy", "preset_ru_banking", "preset_ru_gov", "crash_reports", "zapret_strategy", "zapret_auto_update", "zapret_enabled"}
var legacyIntentKeys = []string{"autoconnect", "last_profile", "last_node", "multihop", "multihop_entry_id", "multihop_exit_id"}

func (p Preferences) MarshalJSON() ([]byte, error) {
	type plain Preferences
	return mergeFields(plain(p), p.AdditionalFields, preferenceKeys)
}
func (p *Preferences) UnmarshalJSON(data []byte) error {
	fields, err := objectFields(data)
	if err != nil {
		return err
	}
	for _, key := range legacyIntentKeys {
		if _, exists := fields[key]; exists {
			return ErrCorruptState
		}
	}
	type plain Preferences
	if err := validateShape(data, reflect.TypeOf(plain{}), 0); err != nil {
		return err
	}
	var value plain
	if json.Unmarshal(data, &value) != nil {
		return ErrCorruptState
	}
	for _, key := range preferenceKeys {
		delete(fields, key)
	}
	if len(fields) == 0 {
		fields = nil
	}
	*p = Preferences(value)
	p.AdditionalFields = fields
	return nil
}

func cloneSnapshot(value Snapshot) (Snapshot, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return Snapshot{}, ErrCorruptState
	}
	var result Snapshot
	if err := json.Unmarshal(data, &result); err != nil {
		return Snapshot{}, err
	}
	return result, nil
}

func equalJSON(a, b any) bool {
	canonical := func(value any) ([]byte, error) {
		data, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()
		var decoded any
		if err := decoder.Decode(&decoded); err != nil {
			return nil, err
		}
		return json.Marshal(decoded)
	}
	left, err := canonical(a)
	if err != nil {
		return false
	}
	right, err := canonical(b)
	return err == nil && bytes.Equal(left, right)
}
