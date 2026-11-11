package singbox

import (
	"errors"
	"testing"

	"github.com/Divaaaan/tenebra/core/model"
	"github.com/Divaaaan/tenebra/core/routing"
)

func TestBuilderBudgetRejectsBeforeOutboundFanout(t *testing.T) {
	nodes := make([]model.Node, 2049)
	for i := range nodes {
		nodes[i] = model.Node{Protocol: model.Trojan, Name: "same", Server: "example.test", Port: 443, Password: "SYNTHETIC_ONLY"}
	}
	config, err := Build(nodes, "", routing.Options{}, TunOptions{})
	var budget *model.BudgetError
	if !errors.As(err, &budget) || budget.Kind != "retained_nodes" || config != nil {
		t.Fatalf("over-budget builder materialized config: nil=%v error=%v", config == nil, err)
	}
	probe, bindings, err := BuildProbe(nodes, 25000)
	if !errors.As(err, &budget) || probe != nil || bindings != nil {
		t.Fatal("probe budget was checked after outbound/listener fan-out")
	}
	if tags := SelectorTags(nodes); len(tags) != 0 {
		t.Fatal("direct selector caller bypassed fan-out budget")
	}
}
