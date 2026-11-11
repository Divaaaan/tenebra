package subscription

import (
	"encoding/json"
	"errors"
	"runtime"
	"strings"
	"testing"

	"github.com/Divaaaan/tenebra/core/model"
)

func TestClashBudgetBeforeGraphExpansion(t *testing.T) {
	proxy := "{name: n, type: trojan, server: example.test, port: 443, password: SYNTHETIC_ONLY}"
	for _, flow := range []bool{false, true} {
		var body string
		if flow {
			body = "proxies: [" + strings.Repeat(proxy+",", 4096) + proxy + "]\n"
		} else {
			body = "proxies:\n" + strings.Repeat("  - "+proxy+"\n", 4097)
		}
		nodes, _, err := ParseSubscription([]byte(body))
		var budget *BudgetError
		if !errors.As(err, &budget) || budget.Kind != "raw_entries" || nodes != nil {
			t.Fatalf("flow=%v allocated too many proxy records: nodes=%d error=%v", flow, len(nodes), err)
		}
	}
	// A broad, irrelevant graph must stay text. This is a large margin above
	// body copies and one normalized node, and below the old graph's allocation.
	body := []byte("rules: [" + strings.Repeat("{a: [x, x, x]},", 30000) + "]\nproxies:\n  - " + proxy + "\n")
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	nodes, _, err := ParseSubscription(body)
	runtime.ReadMemStats(&after)
	if err != nil || len(nodes) != 1 {
		t.Fatalf("ignored graph changed proxy: count=%d error=%v", len(nodes), err)
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 8<<20 {
		t.Fatalf("ignored YAML graph was materialized: allocated=%d", allocated)
	}
}

func TestLimitsCanOnlyReduceAndBodyNeverTruncates(t *testing.T) {
	limits := DefaultLimits()
	if limits != (Limits{8 << 20, 4096, 2048, 16 << 10, 8 << 10}) {
		t.Fatal("default budgets changed")
	}
	for _, invalid := range []Limits{{0, 4096, 2048, 16 << 10, 8 << 10}, {8 << 20, 4097, 2048, 16 << 10, 8 << 10}, {8 << 20, 4096, 2049, 16 << 10, 8 << 10}, {8 << 20, 4096, 2048, 16 << 10, 8193}} {
		if _, _, err := ParseSubscriptionWithLimits(nil, invalid); err == nil {
			t.Fatal("invalid/enlarged limits accepted")
		}
	}
	limits.BodyBytes = 16
	if nodes, _, err := ParseSubscriptionWithLimits([]byte(strings.Repeat("x", 17)), limits); nodes != nil || err == nil {
		t.Fatal("body budget silently truncated")
	}
	limits = DefaultLimits()
	limits.RetainedNodes = 1
	if _, _, err := ParseSubscriptionWithLimits(makeTrojanLinks(2, true), limits); err == nil {
		t.Fatal("caller reduced retained budget was ignored")
	}
	limits = DefaultLimits()
	limits.FieldBytes = 4
	node := model.Node{Protocol: model.Trojan, Name: "name", Server: "host", Port: 443, Password: "five!"}
	if _, err := ValidateNodes([]model.Node{node}, limits); err == nil {
		t.Fatal("caller reduced field budget ignored")
	}
}

func TestNodeAndUnknownJSONBytesUseEscapedAndUTF8Sizes(t *testing.T) {
	limits := DefaultLimits()
	base := model.Node{Protocol: model.Trojan, Name: "n", Server: "host", Port: 443, Password: "p"}
	node := base
	node.AdditionalFields = map[string]json.RawMessage{"future": json.RawMessage(`{"nested":"` + strings.Repeat("я", 4097) + `"}`)}
	if _, err := ValidateNodes([]model.Node{node}, limits); err == nil {
		t.Fatal("nested unknown UTF8 bytes bypassed field budget")
	}
	node = base
	node.Name = strings.Repeat("<", 2700) // JSON escapes each '<' to six bytes.
	if _, err := ValidateNodes([]model.Node{node}, limits); err != nil {
		t.Fatalf("encoded under16KiB node refused: %v", err)
	}
	node.Name = strings.Repeat("<", 2731)
	if _, err := ValidateNodes([]model.Node{node}, limits); err == nil {
		t.Fatal("escaped encoded bytes bypassed node budget")
	}
}
