package singbox

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/Divaaaan/tenebra/core/model"
	"github.com/Divaaaan/tenebra/core/routing"
)

// checkNodes returns a minimal, checker-clean node set: a VLESS-over-TLS node and
// a Hysteria2 node. It deliberately omits the AmneziaWG and REALITY nodes from
// fakeNodes, whose placeholder key material `sing-box check` base64-decodes and
// rejects — a fault in the fake keys, not in the routing block under test.
func checkNodes() []model.Node {
	return []model.Node{
		{
			Protocol: model.VLESS,
			Name:     "vless-ws",
			Server:   "ws.example.test",
			Port:     443,
			UUID:     "22222222-2222-2222-2222-222222222222",
			TLS:      &model.TLS{Enabled: true, ServerName: "ws.example.test"},
			Transport: &model.Transport{
				Type: "ws",
				Path: "/path",
				Host: "ws.example.test",
			},
		},
		{
			Protocol: model.Hysteria2,
			Name:     "hy2",
			Server:   "hy.example.test",
			Port:     8443,
			Password: "hy2pass",
			TLS:      &model.TLS{Enabled: true, ServerName: "hy.example.test"},
		},
	}
}

// findSingBox locates a real sing-box binary to validate generated configs
// against. It walks up from the test's working directory to the bundled
// resources (where fetch-resources drops sing-box next to the .srs files), then
// falls back to a repo-root bin/, then to PATH. It returns the binary path and
// the directory holding the bundled rule-sets (empty when the binary came from
// PATH), or ok=false when no binary is available — the caller then skips, so CI
// without the (gitignored) binary is green rather than failing.
func findSingBox() (bin, ruleSetDir string, ok bool) {
	name := "sing-box"
	if runtime.GOOS == "windows" {
		name = "sing-box.exe"
	}

	dir, err := os.Getwd()
	if err == nil {
		for i := 0; i < 6; i++ {
			cand := filepath.Join(dir, "ui-desktop", "src-tauri", "resources", name)
			if _, statErr := os.Stat(cand); statErr == nil {
				return cand, filepath.Dir(cand), true
			}
			binCand := filepath.Join(dir, "bin", name)
			if _, statErr := os.Stat(binCand); statErr == nil {
				return binCand, "", true
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}

	if p, lookErr := exec.LookPath(name); lookErr == nil {
		return p, "", true
	}
	return "", "", false
}

// singBoxCheck writes cfg to a temp file and runs `sing-box check` on it, failing
// the test (with the tool's own output) when the config is rejected. This is the
// real validator, so it catches schema slips the offline unit tests can't — a
// dangling rule-set tag, a bad server type, or (the 1.13 FATAL this feature must
// never reintroduce) a missing default_domain_resolver.
func singBoxCheck(t *testing.T, bin string, cfg map[string]any) {
	t.Helper()
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	out, err := exec.Command(bin, "check", "-c", path).CombinedOutput()
	if err != nil {
		t.Fatalf("sing-box check rejected the config: %v\n%s\nconfig:\n%s", err, out, raw)
	}
}

// TestRulesConfigPassesSingBoxCheck feeds configs carrying the custom domain
// rules and the RU presets through a real `sing-box check`. It runs the global
// case (no rule-set file dependency) and, when the bundled .srs are present, the
// smart case (custom rules interleaved with the geo rule-sets). Skipped when no
// sing-box binary is available.
func TestRulesConfigPassesSingBoxCheck(t *testing.T) {
	bin, ruleSetDir, ok := findSingBox()
	if !ok {
		t.Skip("sing-box binary not found (resources/ or bin/ or PATH); skipping real config check")
	}

	// Global mode needs no geo rule-set files, so it always runs: it exercises the
	// custom direct/proxy rules and both presets in isolation.
	global, err := Build(checkNodes(), "", routing.Options{
		Mode:            routing.ModeGlobal,
		RulesDirect:     []string{"bank.example", "sberbank.ru"},
		RulesProxy:      []string{"work.example"},
		PresetRuBanking: true,
		PresetRuGov:     true,
	}, TunOptions{})
	if err != nil {
		t.Fatalf("build global config: %v", err)
	}
	t.Run("global", func(t *testing.T) { singBoxCheck(t, bin, global) })

	// Smart mode references the geo rule-sets. With the bundled .srs present it is a
	// local rule-set the checker can load; without them, skip this sub-case rather
	// than have the checker fault on missing files.
	if ruleSetDir == "" {
		t.Skip("no bundled rule-set dir; skipping the smart-mode (geo) config check")
	}
	if _, statErr := os.Stat(filepath.Join(ruleSetDir, "geoip-ru.srs")); statErr != nil {
		t.Skip("bundled geoip-ru.srs missing; skipping the smart-mode (geo) config check")
	}
	smart, err := Build(checkNodes(), "", routing.Options{
		Mode:            routing.ModeSmart,
		RuleSetDir:      ruleSetDir,
		RulesDirect:     []string{"bank.example"},
		RulesProxy:      []string{"work.example"},
		PresetRuBanking: true,
		SplitMode:       routing.SplitExclude,
		SplitApps:       []string{"chrome.exe"},
	}, TunOptions{})
	if err != nil {
		t.Fatalf("build smart config: %v", err)
	}
	t.Run("smart", func(t *testing.T) { singBoxCheck(t, bin, smart) })
}
