package control

import (
	"encoding/json"
	"reflect"

	"github.com/Divaaaan/tenebra/core/profile"
	"github.com/Divaaaan/tenebra/core/routing"
	"github.com/Divaaaan/tenebra/core/singbox"
	"github.com/Divaaaan/tenebra/core/state"
)

// configIntent is private engine input. Its equality excludes process-owned
// bindings and display/scheduling metadata; it never becomes a wire DTO.
type configIntent struct {
	routing     routing.Options
	tun         singbox.TunOptions
	connection  state.ConnectionIntent
	multihop    state.MultihopRequirement
	nodeRecords []string
}

func (configIntent) String() string                       { return "<private-config-intent>" }
func (configIntent) GoString() string                     { return "<private-config-intent>" }
func (intent configIntent) Equal(other configIntent) bool { return reflect.DeepEqual(intent, other) }

func captureConfigIntent(ro routing.Options, tun singbox.TunOptions, connection state.ConnectionIntent, multihop state.MultihopRequirement) configIntent {
	ro = ro.Normalize()
	ro.MultihopEntry, ro.MultihopExit, ro.RuleSetDir = "", "", ""
	ro.SplitApps = append([]string(nil), ro.SplitApps...)
	ro.RulesDirect = append([]string(nil), ro.RulesDirect...)
	ro.RulesProxy = append([]string(nil), ro.RulesProxy...)
	ro.ZapretCovered = append([]string(nil), ro.ZapretCovered...)
	tun = tun.Normalized()
	tun.ClashAPIPort, tun.Address, tun.Address6, tun.CacheDir, tun.Maintenance = 0, "", "", "", nil
	connection.Autoconnect = false
	bindings := make([]state.Binding, 0, 1)
	for _, binding := range multihop.Bindings {
		if multihop.Enabled && binding.ProfileID == connection.ProfileID {
			bindings = append(bindings, binding)
		}
	}
	multihop.Bindings = bindings
	return configIntent{routing: ro, tun: tun, connection: connection, multihop: multihop}
}

// connectionIntent only reads an owned repository snapshot. No clock, random
// source, runtime endpoint, filesystem, or daemon lock participates.
func connectionIntent(snapshot state.Snapshot) (configIntent, error) {
	if err := snapshot.Validate(); err != nil {
		return configIntent{}, err
	}
	p := snapshot.Preferences
	mode, apps := splitFromSettings(persistedSettings{SplitMode: p.SplitMode, SplitApps: p.SplitApps})
	ro := routing.Options{
		Mode: routing.Mode(p.RoutingMode), SplitMode: mode, SplitApps: apps, KillSwitch: p.KillSwitch, TLSFragment: p.TLSFragment,
		AdBlock: p.AdBlock, IPv4Only: p.IPv4Only, DNSRemote: p.DNSRemote, DNSDirect: p.DNSDirect,
		RulesDirect: p.RulesDirect, RulesProxy: p.RulesProxy, PresetRuBanking: p.PresetRuBanking, PresetRuGov: p.PresetRuGov,
		GamesDirect: p.PresetGamesDirect != nil && *p.PresetGamesDirect, VoiceDirect: p.PresetVoiceDirect != nil && *p.PresetVoiceDirect,
		UnblockServices: p.PresetUnblockServices == nil || *p.PresetUnblockServices, Multihop: snapshot.Multihop.Enabled,
	}
	tun := singbox.TunOptions{Mode: p.ProxyMode, MixedPort: p.ProxyPort, Stack: p.TunStack}
	intent := captureConfigIntent(ro, tun, snapshot.Connection, snapshot.Multihop)
	if selected, ok := snapshot.Profiles[snapshot.Connection.ProfileID]; ok {
		var err error
		intent.nodeRecords, err = configNodeRecords(selected)
		if err != nil {
			return configIntent{}, err
		}
	}
	return intent, nil
}

func configNodeRecords(selected profile.Profile) ([]string, error) {
	var records []string
	for _, server := range selected.Servers {
		node := server.Node
		node.Name = ""
		data, err := json.Marshal(node)
		if err != nil {
			return nil, state.ErrCorruptState
		}
		records = append(records, server.ID+":"+string(data))
	}
	return records, nil
}
