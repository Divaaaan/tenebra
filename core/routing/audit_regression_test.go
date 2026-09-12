package routing

import "testing"

func TestSplitOffGamesPresetIgnoresSavedCustomApps(t *testing.T) {
	for _, mode := range []SplitMode{SplitOff, SplitExclude, SplitInclude} {
		t.Run(string(mode), func(t *testing.T) {
			o := (Options{Mode: ModeGlobal, SplitMode: mode, SplitApps: []string{"chrome.exe"}, GamesDirect: true}).Normalize()
			for _, layer := range []struct {
				name, target string
				rules        []map[string]any
			}{
				{"route", "outbound", o.RouteRules()}, {"dns", "server", o.dnsRules()},
			} {
				custom, game := "", ""
				for _, rule := range layer.rules {
					apps, _ := rule["process_name"].([]string)
					if contains(apps, "chrome.exe") {
						custom, _ = rule[layer.target].(string)
					}
					if contains(apps, "steam.exe") {
						game, _ = rule[layer.target].(string)
					}
				}
				direct, proxy := tagDirect, tagProxy
				if layer.name == "dns" {
					direct, proxy = dnsDirectTag, dnsRemoteTag
				}
				wantCustom, wantGame := "", direct
				if mode == SplitExclude {
					wantCustom = direct
				}
				if mode == SplitInclude {
					wantCustom, wantGame = proxy, ""
				}
				if custom != wantCustom || game != wantGame {
					t.Errorf("%s: custom=%q game=%q; want %q/%q", layer.name, custom, game, wantCustom, wantGame)
				}
			}
		})
	}
}

func TestBypassKeepsProxyPinsWhenDirectIsForbidden(t *testing.T) {
	for _, kill := range []bool{false, true} {
		o := (Options{Mode: ModeSmart, KillSwitch: kill, ZapretActive: true, UnblockServices: true}).Normalize()
		for _, layer := range []struct {
			name, target, direct, proxy string
			rules                       []map[string]any
		}{
			{"route", "outbound", tagDirect, tagProxy, o.RouteRules()},
			{"dns", "server", dnsDirectTag, dnsRemoteTag, o.dnsRules()},
		} {
			var got []string
			for _, rule := range layer.rules {
				if contains(suffixesOf(rule), "googlevideo.com") {
					got = append(got, rule[layer.target].(string))
				}
			}
			want := layer.direct
			if kill {
				want = layer.proxy
			}
			if len(got) != 1 || got[0] != want {
				t.Errorf("kill=%v %s: googlevideo targets=%v, want [%s]", kill, layer.name, got, want)
			}
		}
	}
}
