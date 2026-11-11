package singbox

import (
	"context"
	"reflect"
	"testing"

	"github.com/Divaaaan/tenebra/core/maintenance"
	"github.com/Divaaaan/tenebra/core/model"
	"github.com/Divaaaan/tenebra/core/routing"
)

func maintenanceTestNodes(names ...string) []model.Node {
	nodes := make([]model.Node, len(names))
	for i, name := range names {
		nodes[i] = model.Node{Name: name, Protocol: model.Trojan, Server: "203.0.113.8", Port: 443, Password: "synthetic-node-password"}
	}
	return nodes
}

func TestBuilderReservedTagsUseOneAllocator(t *testing.T) {
	nodes := maintenanceTestNodes("proxy", "direct", "block", "maintenance-in", "proxy", "proxy-2", "tun-in", "mixed-in", "dns-remote", "probe-in-0")
	want := []string{"proxy-2", "direct-2", "block-2", "maintenance-in-2", "proxy-3", "proxy-2-2", "tun-in-2", "mixed-in-2", "dns-remote-2", "probe-in-0-2"}
	selected := SelectorTags(nodes)
	got := make([]string, len(selected))
	for i, node := range selected {
		got[i] = node.Tag
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tags = %v; want %v", got, want)
	}
	config, err := Build(nodes, "", routing.Options{}, TunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, object := range config["outbounds"].([]map[string]any) {
		tag := object["tag"].(string)
		if seen[tag] {
			t.Fatal("duplicate outbound tag")
		}
		seen[tag] = true
	}
	_, bindings, err := BuildProbe(nodes, 32000)
	if err != nil {
		t.Fatal(err)
	}
	for i, binding := range bindings {
		if binding.Tag != want[i] {
			t.Fatal("probe assigns different node tags")
		}
	}
}

func TestMaintenanceInboundOptInAndVPNRoute(t *testing.T) {
	runtime, err := maintenance.PrepareRuntime(context.Background(), maintenance.RuntimeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	private := runtime.InboundConfig()
	nodes := maintenanceTestNodes("entry", "exit")
	base, err := Build(nodes, "", routing.Options{}, TunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(base["inbounds"].([]map[string]any)) != 1 {
		t.Fatal("default inbound changed")
	}
	options := routing.Options{Multihop: true, MultihopEntry: "entry", MultihopExit: "exit"}
	config, err := Build(nodes, "", options, TunOptions{Maintenance: &private})
	if err != nil {
		t.Fatal(err)
	}
	inbounds := config["inbounds"].([]map[string]any)
	if len(inbounds) != 2 {
		t.Fatal("private inbound absent")
	}
	inbound := inbounds[1]
	if inbound["type"] != "http" || inbound["tag"] != "maintenance-in" || inbound["listen"] != "127.0.0.1" || inbound["listen_port"] != int(private.Address.Port()) {
		t.Fatal("invalid private inbound")
	}
	if inbound["tls"].(map[string]any)["enabled"] != true {
		t.Fatal("private inbound is not TLS")
	}
	rules := config["route"].(map[string]any)["rules"].([]map[string]any)
	if len(rules) < 2 || !reflect.DeepEqual(rules[0]["inbound"], []string{"maintenance-in"}) || rules[0]["network"] != "tcp" || rules[0]["outbound"] != "proxy" || rules[1]["action"] != "reject" || !reflect.DeepEqual(rules[1]["inbound"], []string{"maintenance-in"}) {
		t.Fatal("control routes are not terminal before user policy")
	}
	selectors := 0
	for _, outbound := range config["outbounds"].([]map[string]any) {
		if outbound["type"] == "selector" {
			selectors++
			if !reflect.DeepEqual(outbound["outbounds"], []string{"exit"}) {
				t.Fatal("private traffic lost full multihop chain")
			}
		}
		if outbound["tag"] == "exit" && outbound["detour"] != "entry" {
			t.Fatal("missing entry detour")
		}
	}
	if selectors != 1 {
		t.Fatal("private traffic has another selector")
	}
}
