// Package protection describes the persistent host guard without performing any
// OS operations. Only the production composition root installs a native Backend.
package protection

import "fmt"

type Layer uint8

const (
	Connect4 Layer = iota
	Connect6
	Accept4
	Accept6
)

var Layers = [...]Layer{Connect4, Connect6, Accept4, Accept6}

func (l Layer) Outbound() bool { return l == Connect4 || l == Connect6 }
func (l Layer) IPv6() bool     { return l == Connect6 || l == Accept6 }

type Field uint8

const (
	Loopback Field = iota
	Interface
	Application
	Protocol
	LocalPort
	RemotePort
	RemoteAddress
)
const (
	Core   = "core"
	Engine = "engine"
	DHCP   = "dhcp"
)

type Condition struct {
	Field  Field
	Number uint64
	Text   string
}
type Rule struct {
	Key        string
	Layer      Layer
	Weight     uint64
	Permit     bool
	Conditions []Condition
}

// Policy returns filters in decreasing priority. OR is expressed as separate
// filters; all conditions within a filter are AND. No caller application, LAN
// range, resolver, or DIRECT split exception is an input to this policy.
func Policy(tunLUID uint64) []Rule {
	var rules []Rule
	for _, l := range Layers {
		add := func(key string, weight uint64, permit bool, c ...Condition) {
			rules = append(rules, Rule{fmt.Sprintf("%d/%s", l, key), l, weight, permit, c})
		}
		add("loopback", 100, true, Condition{Field: Loopback})
		if tunLUID != 0 {
			add("tun", 90, true, Condition{Field: Interface, Number: tunLUID})
		}
		portField := RemotePort
		if !l.Outbound() {
			portField = LocalPort
		}
		for _, proto := range []uint64{6, 17} {
			add(fmt.Sprintf("dns-%d", proto), 80, false, Condition{Field: Protocol, Number: proto}, Condition{Field: portField, Number: 53})
		}
		for _, app := range []string{Core, Engine} {
			add(app, 70, true, Condition{Field: Application, Text: app})
		}
		local, remote := uint64(68), uint64(67)
		if l.IPv6() {
			local, remote = 546, 547
		}
		add("dhcp", 60, true, Condition{Field: Application, Text: DHCP}, Condition{Field: Protocol, Number: 17}, Condition{Field: LocalPort, Number: local}, Condition{Field: RemotePort, Number: remote})
		if l.IPv6() {
			for typ := uint64(133); typ <= 136; typ++ {
				for i, prefix := range []string{"fe80::/10", "ff02::/16"} {
					add(fmt.Sprintf("ndp-%d-%d", typ, i), 50, true, Condition{Field: Protocol, Number: 58}, Condition{Field: LocalPort, Number: typ}, Condition{Field: RemotePort, Number: 0}, Condition{Field: RemoteAddress, Text: prefix})
				}
			}
		}
		add("block", 1, false)
	}
	return rules
}
