package model

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
)

func TestNodeRecordBudgetBoundaries(t *testing.T) {
	base := Node{Protocol: Trojan, Server: "example.com", Port: 443, Password: "TEST_ONLY_CANARY"}
	tests := []struct {
		name  string
		nodes []Node
		kind  string
		limit int64
	}{
		{"empty", nil, "", 0},
		{"2048 records", make([]Node, 2048), "", 0},
		{"2049 records", make([]Node, 2049), "retained_nodes", 2048},
	}
	for _, size := range []int{8192, 8193} {
		n := base
		n.Name = strings.Repeat("x", size)
		kind := ""
		if size > 8192 {
			kind = "field_bytes"
		}
		tests = append(tests, struct {
			name  string
			nodes []Node
			kind  string
			limit int64
		}{
			"ASCII field " + strconv.Itoa(size), []Node{n}, kind, 8192,
		})
		n.Name = strings.Repeat("я", 4096)
		if size > 8192 {
			n.Name += "x"
		}
		tests = append(tests, struct {
			name  string
			nodes []Node
			kind  string
			limit int64
		}{
			"UTF8 field " + strconv.Itoa(size), []Node{n}, kind, 8192,
		})
	}
	n := base
	n.Name = strings.Repeat("a", 8192)
	encoded, err := json.Marshal(n)
	if err != nil {
		t.Fatal(err)
	}
	remaining := 16384 - len(encoded)
	n.Server += strings.Repeat("b", remaining)
	encoded, err = json.Marshal(n)
	if err != nil || len(encoded) != 16384 {
		t.Fatalf("invalid boundary fixture size: %d, %v", len(encoded), err)
	}
	tests = append(tests, struct {
		name  string
		nodes []Node
		kind  string
		limit int64
	}{"16384 encoded bytes", []Node{n}, "", 0})
	n.Server += "b"
	tests = append(tests, struct {
		name  string
		nodes []Node
		kind  string
		limit int64
	}{"16385 encoded bytes", []Node{n}, "node_bytes", 16384})
	n = base
	n.Name = strings.Repeat("<", 2800)
	tests = append(tests, struct {
		name  string
		nodes []Node
		kind  string
		limit int64
	}{"JSON escaping", []Node{n}, "node_bytes", 16384})
	n = base
	n.TLS = &TLS{ALPN: []string{strings.Repeat("c", 8193)}}
	tests = append(tests, struct {
		name  string
		nodes []Node
		kind  string
		limit int64
	}{"nested array field", []Node{n}, "field_bytes", 8192})
	n = base
	n.Extra = map[string]string{"private": strings.Repeat("TEST_ONLY_CANARY", 600)}
	tests = append(tests, struct {
		name  string
		nodes []Node
		kind  string
		limit int64
	}{"private extra field", []Node{n}, "field_bytes", 8192})
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateNodeRecords(tt.nodes)
			if tt.kind == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var budget *BudgetError
			if !errors.As(err, &budget) || budget.Kind != tt.kind || budget.Limit != tt.limit {
				t.Fatalf("got %v, want %s/%d", err, tt.kind, tt.limit)
			}
			if strings.Contains(err.Error(), "TEST_ONLY_CANARY") {
				t.Fatal("error exposed node contents")
			}
		})
	}
}
