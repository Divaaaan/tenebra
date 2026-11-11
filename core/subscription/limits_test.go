package subscription

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Divaaaan/tenebra/core/model"
)

func makeTrojanLinks(count int, unique bool) []byte {
	var output strings.Builder
	for index := 0; index < count; index++ {
		label := 0
		if unique {
			label = index
		}
		fmt.Fprintf(&output, "trojan://SYNTHETIC_ONLY@example.test:443#node-%d\n", label)
	}
	return []byte(output.String())
}

func TestRetainedNodeLimit(t *testing.T) {
	if nodes, _, err := ParseSubscription(makeTrojanLinks(2048, true)); err != nil || len(nodes) != 2048 {
		t.Fatalf("2048 boundary refused: nodes=%d error=%v", len(nodes), err)
	}
	nodes, _, err := ParseSubscription(makeTrojanLinks(2049, true))
	var budget *model.BudgetError
	if !errors.As(err, &budget) || budget.Kind != "retained_nodes" || budget.Limit != 2048 || nodes != nil {
		t.Fatalf("2049 records escaped budget: count=%d error=%v", len(nodes), err)
	}
}

func TestRawEntriesLimitBeforeDedup(t *testing.T) {
	if nodes, _, err := ParseSubscription(makeTrojanLinks(4096, false)); err != nil || len(nodes) != 1 {
		t.Fatalf("4096 duplicate boundary not exactly deduped: nodes=%d error=%v", len(nodes), err)
	}
	nodes, _, err := ParseSubscription(makeTrojanLinks(4097, false))
	var budget *model.BudgetError
	if !errors.As(err, &budget) || budget.Kind != "raw_entries" || budget.Limit != 4096 || nodes != nil {
		t.Fatalf("4097 raw entries escaped budget: nodes=%d error=%v", len(nodes), err)
	}
	nodes, _, err = ParseSubscription([]byte(strings.Repeat("unsupported://x\n", 4097)))
	if !errors.As(err, &budget) || budget.Kind != "raw_entries" || nodes != nil {
		t.Fatal("unsupported entries bypassed work budget")
	}
}

func TestExactRecordDedupKeepsAliasesAndSNI(t *testing.T) {
	base := "trojan://SYNTHETIC_ONLY@example.test:443?sni=a.test#same"
	input := base + "\n" + base + "\n" + strings.Replace(base, "#same", "#different", 1) + "\n" + strings.Replace(base, "sni=a.test", "sni=b.test", 1)
	nodes, _, err := ParseSubscription([]byte(input))
	if err != nil || len(nodes) != 3 {
		t.Fatalf("alias/SNI lost or exact duplicate kept: count=%d error=%v", len(nodes), err)
	}
}

func TestNodeAndFieldBudgets(t *testing.T) {
	for _, length := range []int{8192, 8193} {
		input := []byte("trojan://" + strings.Repeat("x", length) + "@example.test:443#n")
		nodes, _, err := ParseSubscription(input)
		if length == 8192 {
			if err != nil || len(nodes) != 1 {
				t.Fatal("8192 decoded bytes refused")
			}
			continue
		}
		var budget *model.BudgetError
		if !errors.As(err, &budget) || budget.Kind != "field_bytes" || budget.Limit != 8192 || nodes != nil {
			t.Fatalf("8193 field bytes accepted: count=%d error=%v", len(nodes), err)
		}
		if strings.Contains(err.Error(), "xxxxxxxxxx") {
			t.Fatal("budget error exposed credentials")
		}
	}
}
