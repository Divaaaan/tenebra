package state

import "encoding/json"

// Preferences preserves pointer/unset distinctions from the legacy format.
// Connection and multihop intent have one typed home in Snapshot.
type Preferences struct {
	Version               int                        `json:"version"`
	SplitMode             string                     `json:"split_mode,omitempty"`
	SplitApps             []string                   `json:"split_apps,omitempty"`
	RoutingMode           string                     `json:"routing_mode,omitempty"`
	PresetGamesDirect     *bool                      `json:"preset_games_direct,omitempty"`
	PresetVoiceDirect     *bool                      `json:"preset_voice_direct,omitempty"`
	PresetUnblockServices *bool                      `json:"preset_unblock_services,omitempty"`
	KillSwitch            bool                       `json:"kill_switch,omitempty"`
	TLSFragment           bool                       `json:"tls_fragment,omitempty"`
	TunStack              string                     `json:"tun_stack,omitempty"`
	ProxyMode             string                     `json:"proxy_mode,omitempty"`
	ProxyPort             int                        `json:"proxy_port,omitempty"`
	AutoFailover          *bool                      `json:"auto_failover,omitempty"`
	AdBlock               bool                       `json:"ad_block,omitempty"`
	DNSRemote             string                     `json:"dns_remote,omitempty"`
	DNSDirect             string                     `json:"dns_direct,omitempty"`
	IPv4Only              bool                       `json:"ipv4_only,omitempty"`
	RulesDirect           []string                   `json:"rules_direct,omitempty"`
	RulesProxy            []string                   `json:"rules_proxy,omitempty"`
	PresetRuBanking       bool                       `json:"preset_ru_banking,omitempty"`
	PresetRuGov           bool                       `json:"preset_ru_gov,omitempty"`
	CrashReports          *bool                      `json:"crash_reports,omitempty"`
	ZapretStrategy        string                     `json:"zapret_strategy,omitempty"`
	ZapretAutoUpdate      *bool                      `json:"zapret_auto_update,omitempty"`
	ZapretEnabled         *bool                      `json:"zapret_enabled,omitempty"`
	AdditionalFields      map[string]json.RawMessage `json:"-"`
}
