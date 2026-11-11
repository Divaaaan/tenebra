package nodeidentity

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/Divaaaan/tenebra/core/model"
)

func completeNode(t *testing.T) model.Node {
	t.Helper()
	raw := []byte(`{"protocol":"vless","name":"label","server":"example.test","port":443,"uuid":"uuid","password":"password","method":"method","flow":"flow","security":"security","alter_id":1,"tls":{"enabled":true,"server_name":"sni.test","insecure":true,"alpn":["h2","http/1.1"],"fingerprint":"chrome","fragment":true,"reality":{"public_key":"public","short_id":"short","spider_x":"spider"}},"transport":{"type":"ws","path":"/ws","host":"host.test","service_name":"grpc"},"obfs":{"type":"salamander","password":"obfs"},"wireguard":{"private_key":"private","peer_public_key":"peer","pre_shared_key":"psk","local_address":["10.0.0.1/32","::1/128"],"jc":1,"jmin":2,"jmax":3,"s1":4,"s2":5,"h1":6,"h2":7,"h3":8,"h4":9},"extra":{"a":"one","b":"two"}}`)
	var node model.Node
	if err := json.Unmarshal(raw, &node); err != nil {
		t.Fatal(err)
	}
	return node
}

type fieldMutation struct {
	name string
	path []int
	kind reflect.Kind
}

func modelLeaves(t *testing.T, value reflect.Value, prefix string, path []int) []fieldMutation {
	t.Helper()
	var result []fieldMutation
	if value.Kind() == reflect.Pointer {
		value = value.Elem()
	}
	for index := 0; index < value.NumField(); index++ {
		field := value.Type().Field(index)
		if field.Name == "AdditionalFields" {
			continue
		}
		child := value.Field(index)
		name := prefix + field.Name
		indices := append(append([]int(nil), path...), index)
		if child.IsZero() {
			t.Fatalf("complete fixture must populate new model field %s", name)
		}
		if child.Kind() == reflect.Pointer {
			result = append(result, fieldMutation{name + "/nil", indices, reflect.Pointer})
			result = append(result, modelLeaves(t, child, name+".", indices)...)
			continue
		}
		result = append(result, fieldMutation{name, indices, child.Kind()})
	}
	return result
}

func fieldAt(value reflect.Value, indices []int) reflect.Value {
	for _, index := range indices {
		if value.Kind() == reflect.Pointer {
			value = value.Elem()
		}
		value = value.Field(index)
	}
	return value
}

func TestIdentityMatcherV1AllNodeFieldsExceptName(t *testing.T) {
	base := completeNode(t)
	matcher, err := NewMatcher([]model.Node{base})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(base)
	for _, mutation := range modelLeaves(t, reflect.ValueOf(base), "Node.", nil) {
		t.Run(mutation.name, func(t *testing.T) {
			var candidate model.Node
			json.Unmarshal(data, &candidate)
			value := fieldAt(reflect.ValueOf(&candidate).Elem(), mutation.path)
			switch mutation.kind {
			case reflect.String:
				value.SetString(value.String() + "changed")
			case reflect.Bool:
				value.SetBool(!value.Bool())
			case reflect.Int, reflect.Int64:
				value.SetInt(value.Int() + 1)
			case reflect.Pointer:
				value.Set(reflect.Zero(value.Type()))
			case reflect.Slice:
				value.Index(0).SetString(value.Index(0).String() + "changed")
			case reflect.Map:
				key := reflect.ValueOf("a")
				value.SetMapIndex(key, reflect.ValueOf("changed"))
			default:
				t.Fatalf("add mutation support for actual model kind %s", mutation.kind)
			}
			matches, err := matcher.Candidates(candidate)
			if err != nil {
				t.Fatal(err)
			}
			want := 0
			if mutation.name == "Node.Name" {
				want = 1
			}
			if len(matches) != want {
				t.Fatalf("full-spec matcher ignored %s: candidates=%v", mutation.name, matches)
			}
		})
	}
	// Lists retain order even though maps do not.
	for _, name := range []string{"ALPN", "local_address"} {
		t.Run(name+"_order", func(t *testing.T) {
			var candidate model.Node
			json.Unmarshal(data, &candidate)
			list := candidate.TLS.ALPN
			if name == "local_address" {
				list = candidate.WireGuard.LocalAddress
			}
			list[0], list[1] = list[1], list[0]
			matches, _ := matcher.Candidates(candidate)
			if len(matches) != 0 {
				t.Fatal("list order was erased")
			}
		})
	}
}

func TestIdentityMatcherRetainsUnknownAtEveryObjectAndCanonicalMapOrder(t *testing.T) {
	for _, name := range []string{"node", "tls", "reality", "transport", "obfs", "wireguard"} {
		t.Run(name, func(t *testing.T) {
			base := completeNode(t)
			fields := map[string]json.RawMessage{"future": json.RawMessage(`{"z":1,"a":[true,"x"]}`)}
			switch name {
			case "node":
				base.AdditionalFields = fields
			case "tls":
				base.TLS.AdditionalFields = fields
			case "reality":
				base.TLS.Reality.AdditionalFields = fields
			case "transport":
				base.Transport.AdditionalFields = fields
			case "obfs":
				base.Obfs.AdditionalFields = fields
			case "wireguard":
				base.WireGuard.AdditionalFields = fields
			}
			matcher, err := NewMatcher([]model.Node{base})
			if err != nil {
				t.Fatal(err)
			}
			fields["future"] = json.RawMessage(`{"a":[true,"x"],"z":1}`)
			matches, err := matcher.Candidates(base)
			if err != nil || len(matches) != 1 {
				t.Fatal("unknown map insertion order changed identity")
			}
			fields["future"] = json.RawMessage(`{"a":[true,"DIFFERENT"],"z":1}`)
			matches, err = matcher.Candidates(base)
			if err != nil || len(matches) != 0 {
				t.Fatal("unknown nested connection field was ignored")
			}
		})
	}
}

func TestIdentityRecordDedupIncludesLabel(t *testing.T) {
	a := completeNode(t)
	b := a
	b.Name = "other label"
	data, _ := json.Marshal(a)
	var sni model.Node
	json.Unmarshal(data, &sni)
	sni.TLS.ServerName = "other-sni.test"
	got, err := DeduplicateRecords([]model.Node{a, a, b, sni})
	if err != nil || len(got) != 3 {
		t.Fatalf("records=%d error=%v", len(got), err)
	}
	if got, err := DeduplicateRecords(nil); err != nil || got != nil {
		t.Fatal("nil input was not preserved")
	}
	matcher, _ := NewMatcher([]model.Node{a})
	first, _ := matcher.Candidates(a)
	first[0] = 123
	again, _ := matcher.Candidates(a)
	if again[0] != 0 {
		t.Fatal("caller mutated matcher state")
	}
}
